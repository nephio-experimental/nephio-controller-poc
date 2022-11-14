/*
Copyright 2022 The Nephio Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package automation

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	configapi "github.com/GoogleContainerTools/kpt/porch/api/porchconfig/v1alpha1"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer/json"
	"k8s.io/apimachinery/pkg/types"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"sigs.k8s.io/kustomize/kyaml/resid"
	"sigs.k8s.io/kustomize/kyaml/yaml"

	"github.com/go-logr/logr"
	"golang.org/x/mod/semver"

	automationv1alpha1 "github.com/nephio-project/nephio-controller-poc/apis/automation/v1alpha1"
	infrav1alpha1 "github.com/nephio-project/nephio-controller-poc/apis/infra/v1alpha1"

	kptfile "github.com/GoogleContainerTools/kpt/pkg/api/kptfile/v1"
	porchv1alpha1 "github.com/GoogleContainerTools/kpt/porch/api/porch/v1alpha1"
	"github.com/nephio-project/nephio-controller-poc/pkg/porch"
)

// namespace->repo->package->revision
type PackageRevisionMapByRev map[string][]*porchv1alpha1.PackageRevision
type PackageRevisionMapByPkg map[string]PackageRevisionMapByRev
type PackageRevisionMapByRepo map[string]PackageRevisionMapByPkg
type PackageRevisionMapByNS map[string]PackageRevisionMapByRepo

// PackageDeploymentReconciler reconciles a PackageDeployment object
type PackageDeploymentReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	PorchClient client.Client

	// NOTE: this should be updated with every request, it sucks
	packageRevs PackageRevisionMapByNS
	l           logr.Logger
	s           *json.Serializer
}

//+kubebuilder:rbac:groups=automation.nephio.org,resources=packagedeployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=automation.nephio.org,resources=packagedeployments/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=automation.nephio.org,resources=packagedeployments/finalizers,verbs=update
//+kubebuilder:rbac:groups=infra.nephio.org,resources=clusters,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the PackageDeployment object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.12.2/pkg/reconcile
func (r *PackageDeploymentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	pd, err := r.startRequest(ctx, req)
	if err != nil {
		if client.IgnoreNotFound(err) != nil {
			r.l.Error(err, "cannot get pd")
			return ctrl.Result{}, err
		}
		r.l.Info("cannot get resource, probably deleted", "error", err.Error())
		return ctrl.Result{}, nil
	}

	// Find the clusters matching the selector
	selector, err := metav1.LabelSelectorAsSelector(pd.Spec.Selector)
	if err != nil {
		r.l.Error(err, "could not create selector", "pd", pd)
		return ctrl.Result{}, err
	}

	var clusterList infrav1alpha1.ClusterList
	if err := r.List(ctx, &clusterList, client.MatchingLabelsSelector{Selector: selector}); err != nil {
		r.l.Error(err, "could not list clusters", "selector", selector)
		return ctrl.Result{}, err
	}

	if len(clusterList.Items) == 0 {
		r.l.Info("No clusters for PackageDeployment", "name", req.NamespacedName)
		return ctrl.Result{}, nil
	}

	r.l.Info("Found clusters for PackageDeployment", "name", req.NamespacedName, "clusters", len(clusterList.Items))

	packageNS := pd.Namespace
	if pd.Spec.PackageRef.Namespace != "" {
		packageNS = pd.Spec.PackageRef.Namespace
	}

	sourcePR := r.findPackageRevision(packageNS, pd.Spec.PackageRef.RepositoryName, pd.Spec.PackageRef.PackageName, pd.Spec.PackageRef.Revision)

	if sourcePR == nil {
		r.l.Info("Could not find matching package revision")
		return ctrl.Result{RequeueAfter: 1 * time.Minute}, nil
	}

	r.l.Info("Found matching package revision", "pr", sourcePR)

	// WARNING WARNING WARNING
	// NOTE: this is bad, it's only looking at "first run" - this is PROOF OF CONCEPT
	// code, not production code. What we MUST do in a real controller is:
	//  - (1) Identify all PackageRevisions that should exist based on the current spec
	//  - (2) Identify existing PackageRevisions created by this controller
	//  - (3) Prune PackageRevisions we created that no long are part of this spec
	//  - (4) Create new PackageRevisions for new matches
	//  - (5) Verify the version for existing matches
	//
	// This code creates new PackageRevisions for new matches and performs updates
	// on existing matches (1, 4, and 5) . It does not identify existing PackageRevisions nor
	// does it do pruning (2 and 3).

	// For each cluster, we want to create a variant of the package
	// in the associated repository. There are two mutations we will
	// make to the package when rendering the variant: 1) we will inject
	// in the Namespace resource if it is not present; 2) if the package
	// contains a ClusterScaleProfile, and the cluster has an associated
	// ClusterScaleProfile, we will overwrite the package version with the
	// associated cluster version.
	//
	//
	targetNS := "default"
	if pd.Spec.Namespace != nil {
		targetNS = *pd.Spec.Namespace
	}

	for _, c := range clusterList.Items {
		// Clone or update the package from upstream to the repo
		newPR, err := r.ensurePackageRevision(ctx, pd, &c, sourcePR)
		if err != nil {
			r.l.Error(err, "could not clone package", "pd", pd, "cluster", c)
			continue
		}

		if newPR == nil {
			continue
		}

		if err := r.applyPackageMutations(ctx, targetNS, &c, newPR); err != nil {
			r.l.Error(err, "could not apply package mutations")
			continue
		}

		// Propose the package
	}

	return ctrl.Result{}, nil
}

func (r *PackageDeploymentReconciler) applyPackageMutations(ctx context.Context,
	targetNS string,
	c *infrav1alpha1.Cluster,
	newPR *porchv1alpha1.PackageRevision) error {

	// Load the contents of the new variant
	resources, err := r.loadResourceList(ctx, newPR)
	if err != nil {
		r.l.Error(err, "could not load resources", "newPR", newPR, "cluster", c)
		return err
	}

	pkgBuf, err := porch.ResourcesToPackageBuffer(resources.Spec.Resources)
	if err != nil {
		r.l.Error(err, "could not parse resources", "newPR", newPR, "cluster", c)
		return err
	}

	r.l.Info("parsed resources", "resources", pkgBuf)

	nsIdx := -1
	bindingResources := make(map[yaml.ResourceIdentifier]*yaml.RNode)
	for i, n := range pkgBuf.Nodes {
		// search for namespace
		if n.GetApiVersion() == "v1" && n.GetKind() == "Namespace" {
			nsIdx = i
		}

		// search for binding resources
		annotations := n.GetAnnotations()

		if value, found := annotations["automation.nephio.org/config-injection"]; found {
			valueAsBool, err := strconv.ParseBool(value)
			if err == nil && valueAsBool {
				id := yaml.ResourceIdentifier{
					TypeMeta: yaml.TypeMeta{
						APIVersion: n.GetApiVersion(),
						Kind:       n.GetKind(),
					},
					NameMeta: yaml.NameMeta{
						Name:      n.GetName(),
						Namespace: n.GetNamespace(),
					},
				}
				bindingResources[id] = n
			}
		}
	}

	// TODO: Add set-namespace function if it doesn't already exist
	// Add a Namespace if it does not already exist in the package
	if nsIdx == -1 {
		r.l.Info("No namespace found in package, adding one")

		nsNode, err := yaml.Parse(`apiVersion: v1
kind: Namespace
metadata:
  name: ` + targetNS)
		if err != nil {
			r.l.Error(err, "could not parse namespace yaml", "targetNS", targetNS)
			return err
		}
		pkgBuf.Nodes = append(pkgBuf.Nodes, nsNode)
	}

	// If a binding resource exists in the package, and one exists
	// that matches the name of the associated cluster, overwrite the
	// resource in the package with the cluster resource
	conditions := convertConditions(newPR.Status.Conditions)

	for id, pkgResource := range bindingResources {
		clusterResource, err := r.findClusterObject(ctx, c, id)
		group, _ := resid.ParseGroupVersion(id.APIVersion)
		conditionType := fmt.Sprintf("%s.%s.%s.Injected", group, id.Kind, id.Name)
		if err != nil {
			r.l.Info(fmt.Sprintf("error looking for cluster resource: %s", err.Error()), "cluster", c)
			meta.SetStatusCondition(conditions, metav1.Condition{Type: conditionType, Status: metav1.ConditionFalse,
				Reason: "ResourceNotFound", Message: fmt.Sprintf(err.Error())})
			continue
		}
		if clusterResource != nil {
			// convert it to a *RNode
			var spYamlBuf bytes.Buffer
			if err := r.s.Encode(clusterResource, &spYamlBuf); err != nil {
				r.l.Error(err, fmt.Sprintf("could not write resource with apiVersion %q and kind %q as yaml",
					id.APIVersion, id.Kind), "clusterResource", clusterResource)
				meta.SetStatusCondition(conditions, metav1.Condition{Type: conditionType, Status: metav1.ConditionFalse,
					Reason: "ResourceEncodeErr", Message: fmt.Sprintf("could not encode resource with apiVersion %q, kind %q, name %q, and namespace %q"+
						" in the cluster: %s", id.APIVersion, id.Kind, c.Name, c.Namespace, err.Error())})
				return err
			}

			spNode, err := yaml.Parse(spYamlBuf.String())
			if err != nil {
				r.l.Error(err, "could not parse cluster object yaml", "spYaml", spYamlBuf.String())
				meta.SetStatusCondition(conditions, metav1.Condition{Type: conditionType, Status: metav1.ConditionFalse,
					Reason: "ResourceParseErr", Message: err.Error()})
				return err
			}

			// set the spec on the one in the package to match our spec
			field := spNode.Field("spec")
			if err := pkgResource.SetMapField(field.Value, "spec"); err != nil {
				str, _ := spNode.String()
				r.l.Error(err, "could not set object spec", "spNode", str)
				meta.SetStatusCondition(conditions, metav1.Condition{Type: conditionType, Status: metav1.ConditionFalse,
					Reason: "ResourceSpecErr", Message: err.Error()})
				return err
			}

			meta.SetStatusCondition(conditions, metav1.Condition{Type: conditionType, Status: metav1.ConditionTrue,
				Reason: "ResourceInjectedFromCluster", Message: fmt.Sprintf("resource with apiVersion %q, kind %q, name %q, and namespace %q injected "+
					"to the package revision from the cluster",
					id.APIVersion, id.Kind, c.Name, c.Namespace)})
		}
	}

	var prConditions []kptfile.Condition
	for _, c := range *conditions {
		prConditions = append(prConditions, kptfile.Condition{
			Type:    c.Type,
			Reason:  c.Reason,
			Status:  kptfile.ConditionStatus(c.Status),
			Message: c.Message,
		})
	}

	for i, n := range pkgBuf.Nodes {
		if n.GetKind() == "Kptfile" {
			// we need to update the status
			nStr := n.MustString()
			var kf kptfile.KptFile
			if err := yaml.Unmarshal([]byte(nStr), &kf); err != nil {
				return err
			}
			if kf.Status == nil {
				kf.Status = &kptfile.Status{}
			}
			kf.Status.Conditions = prConditions

			kfBytes, _ := yaml.Marshal(kf)
			node := yaml.MustParse(string(kfBytes))
			pkgBuf.Nodes[i] = node
		}
	}

	r.l.Info("updated package RNodes", "RNodes", pkgBuf.Nodes)

	// Save the package by creating an updated PackageRevisionResources
	newResources, err := porch.CreateUpdatedResources(resources.Spec.Resources, pkgBuf)
	if err != nil {
		r.l.Error(err, "could create new resource map")
		return err
	}

	resources.Spec.Resources = newResources
	if err := r.PorchClient.Update(ctx, resources); err != nil {
		r.l.Error(err, "could not save updated resources")
		return err
	}

	r.l.Info("updated PackageRevisionResources", "resources", resources)

	return nil
}

func convertConditions(conditions []porchv1alpha1.Condition) *[]metav1.Condition {
	var result []metav1.Condition
	for _, c := range conditions {
		result = append(result, metav1.Condition{
			Type:    c.Type,
			Reason:  c.Reason,
			Status:  metav1.ConditionStatus(c.Status),
			Message: c.Message,
		})
	}
	return &result
}

func (r *PackageDeploymentReconciler) findClusterObject(ctx context.Context, c *infrav1alpha1.Cluster,
	id yaml.ResourceIdentifier) (*unstructured.Unstructured, error) {

	u := &unstructured.Unstructured{}
	group, version := resid.ParseGroupVersion(id.APIVersion)
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   group,
		Kind:    id.Kind,
		Version: version,
	})

	if err := r.Client.Get(ctx, client.ObjectKey{
		Namespace: c.Namespace,
		Name:      c.Name,
	}, u); err != nil {
		r.l.Error(err, "unable to fetch object from cluster", "cluster", c)
		return nil, err
	}

	return u, nil
}

func (r *PackageDeploymentReconciler) startRequest(ctx context.Context, req ctrl.Request) (*automationv1alpha1.PackageDeployment, error) {
	r.l = log.FromContext(ctx)
	r.l.Info("reconcile", "req", req)

	err := r.loadPackageRevisions(ctx)
	if err != nil {
		return nil, err
	}

	// Load the PackageDeployment
	var pd automationv1alpha1.PackageDeployment
	if err := r.Get(ctx, req.NamespacedName, &pd); err != nil {
		r.l.Error(err, "unable to fetch PackageDeployment")
		return nil, client.IgnoreNotFound(err)
	}

	r.s = json.NewSerializerWithOptions(json.DefaultMetaFactory, nil, nil, json.SerializerOptions{Yaml: true})
	return &pd, nil
}

func (r *PackageDeploymentReconciler) loadPackageRevisions(ctx context.Context) error {

	// Try to locate the package
	// Brute force search :(
	var prList porchv1alpha1.PackageRevisionList
	if err := r.PorchClient.List(ctx, &prList); err != nil {
		r.l.Error(err, "could not list package revisions")
		return err
	}

	r.l.Info("Found packages", "count", len(prList.Items))

	packageRevs := make(PackageRevisionMapByNS)
	for _, pr := range prList.Items {
		r.l.Info("Found", "PackageRevision", pr)
		if _, ok := packageRevs[pr.Namespace]; !ok {
			packageRevs[pr.Namespace] = make(PackageRevisionMapByRepo)
		}
		m := packageRevs[pr.Namespace]
		if _, ok := m[pr.Spec.RepositoryName]; !ok {
			m[pr.Spec.RepositoryName] = make(PackageRevisionMapByPkg)
		}
		if _, ok := m[pr.Spec.RepositoryName][pr.Spec.PackageName]; !ok {
			m[pr.Spec.RepositoryName][pr.Spec.PackageName] = make(PackageRevisionMapByRev)
		}
		newPR := pr
		list := m[pr.Spec.RepositoryName][pr.Spec.PackageName][pr.Spec.Revision]
		list = append(list, &newPR)
		m[pr.Spec.RepositoryName][pr.Spec.PackageName][pr.Spec.Revision] = list
	}

	r.packageRevs = packageRevs
	return nil
}

func (r *PackageDeploymentReconciler) findPackageRevision(ns, repo, pkg, rev string) *porchv1alpha1.PackageRevision {
	if r.packageRevs == nil {
		return nil
	}

	if _, ok := r.packageRevs[ns]; !ok {
		return nil
	}
	if _, ok := r.packageRevs[ns][repo]; !ok {
		return nil
	}
	if _, ok := r.packageRevs[ns][repo][pkg]; !ok {
		return nil
	}
	if _, ok := r.packageRevs[ns][repo][pkg][rev]; !ok {
		return nil
	}
	list := r.packageRevs[ns][repo][pkg][rev]
	return list[0]
}

func (r *PackageDeploymentReconciler) ensurePackageRevision(ctx context.Context,
	pd *automationv1alpha1.PackageDeployment,
	c *infrav1alpha1.Cluster,
	sourcePR *porchv1alpha1.PackageRevision) (*porchv1alpha1.PackageRevision, error) {

	// What we do here is:
	//   - Check if the target repo has the package already
	//   - If not, clone it and we're done. Otherwise continue.
	//   - If there is a downstream package draft, look at the draft. Otherwise, look at
	//     the latest published downstream package revision.
	//   - Compare the UPSTREAM (base) revision of the package against
	//     the package in the PackageDeployment. Note that the revision
	//     stored in r.packageRevs will contain the LOCAL (downstream)
	//     revision number of the package, so we CANNOT directly compare
	//     them.
	//   - If the PackageDeployment revision is different from the UPSTREAM
	//     revision of the package, then update the package to that revision
	//     (which could be an upgrade OR downgrade).
	//
	ns := "default"
	if pd.Namespace != "" {
		ns = pd.Namespace
	}

	newPackageName := pd.Name
	if pd.Spec.Namespace != nil {
		newPackageName = *pd.Spec.Namespace
	}
	if pd.Spec.Name != nil {
		newPackageName = *pd.Spec.Name
	}

	r.l.Info("looking for downstream package")

	// check if the package already exists downstream
	if byRepo, ok := r.packageRevs[ns]; ok {
		if byPkg, ok := byRepo[c.RepositoryRef.Name]; ok {
			if byRev, ok := byPkg[newPackageName]; ok {
				// Look for a downstream package revision. We will update either
				// the draft, or the latest published revision.
				// TODO: Use owner refs to identify which package revisions
				// we should look at (when porch is ready).
				var draft *porchv1alpha1.PackageRevision
				var latestPublished *porchv1alpha1.PackageRevision
				// the first package revision number that porch assigns is "v1",
				// so use v0 as a placeholder for now
				latestVersion := "v0"
				for _, pkgList := range byRev {
					pkgRev := pkgList[0]
					if pkgRev.Spec.Lifecycle != porchv1alpha1.PackageRevisionLifecyclePublished {
						draft = pkgRev
					} else {
						switch cmp := semver.Compare(pkgRev.Spec.Revision, latestVersion); {
						case cmp == 0:
							// Same revision.
						case cmp < 0:
							// current < latest; no change
						case cmp > 0:
							// current > latest; update latest
							latestVersion = pkgRev.Spec.Revision
							latestPublished = pkgRev
						}
					}
				}

				if len(byRev) > 0 {
					// If there is a downstream package revision draft, update and
					// return it. We should never end up with more than one draft at a time.
					if draft != nil {
						if upstreamPR := r.discoverUpdate(ctx, draft, pd.Spec.PackageRef.Revision); upstreamPR == nil {
							return draft, nil
						} else {
							return r.updateDraft(ctx, draft, upstreamPR)
						}
					}
					// If no drafts, find the latest published package revision,
					// copy, update, and return it.
					r.l.Info("updating the latest published package revision")
					return r.updateLatest(ctx, latestPublished, pd.Spec.PackageRef.Revision)
				}
			}
		}
	}

	// If no downstream package revisions managed by this controller,
	// create one.

	// We SHOULD be adding an ownerRef with the controller and PD info,
	// This would be to facilitate pruning. I am not sure if the aggregated
	// API server in Porch supports this; if not we need to add it.
	//
	newPR := &porchv1alpha1.PackageRevision{
		TypeMeta: metav1.TypeMeta{
			Kind:       "PackageRevision",
			APIVersion: porchv1alpha1.SchemeGroupVersion.Identifier(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   ns,
			Annotations: pd.Spec.Annotations,
			Labels:      pd.Spec.Labels,
		},
		Spec: porchv1alpha1.PackageRevisionSpec{
			//PackageName:    sourcePR.Spec.PackageName,
			//Revision:       sourcePR.Spec.Revision,
			PackageName:    newPackageName,
			WorkspaceName:  porchv1alpha1.WorkspaceName(fmt.Sprintf("packagedeployment-1")),
			RepositoryName: c.RepositoryRef.Name,
			Tasks: []porchv1alpha1.Task{
				{
					Type: porchv1alpha1.TaskTypeClone,
					Clone: &porchv1alpha1.PackageCloneTaskSpec{
						Upstream: porchv1alpha1.UpstreamPackage{
							UpstreamRef: &porchv1alpha1.PackageRevisionRef{
								Name: sourcePR.Name,
							},
						},
					},
				},
			},
		},
	}
	err := r.PorchClient.Create(ctx, newPR)
	if err != nil {
		return nil, err
	}

	r.l.Info("Created PackageRevision", "newPR", newPR)
	return newPR, nil
}

func (r *PackageDeploymentReconciler) loadResourceList(ctx context.Context, pr *porchv1alpha1.PackageRevision) (*porchv1alpha1.PackageRevisionResources, error) {
	var resources porchv1alpha1.PackageRevisionResources
	if err := r.PorchClient.Get(ctx, client.ObjectKey{
		Namespace: pr.Namespace,
		Name:      pr.Name,
	}, &resources); err != nil {
		return nil, err
	}

	return &resources, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PackageDeploymentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&automationv1alpha1.PackageDeployment{}).
		Watches(&source.Kind{Type: &infrav1alpha1.Cluster{}},
			handler.EnqueueRequestsFromMapFunc(r.mapClustersToRequests)).
		Complete(r)
}

func (r *PackageDeploymentReconciler) mapClustersToRequests(cluster client.Object) []reconcile.Request {
	attachedPackageDeployments := &automationv1alpha1.PackageDeploymentList{}
	err := r.List(context.TODO(), attachedPackageDeployments, &client.ListOptions{
		Namespace: cluster.GetNamespace(),
	})
	if err != nil {
		return []reconcile.Request{}
	}
	requests := make([]reconcile.Request, len(attachedPackageDeployments.Items))
	for i, item := range attachedPackageDeployments.Items {
		requests[i] = reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      item.GetName(),
				Namespace: item.GetNamespace(),
			},
		}
	}
	return requests
}

func (r *PackageDeploymentReconciler) discoverUpdate(ctx context.Context,
	sourcePR *porchv1alpha1.PackageRevision, targetUpstreamRevision string) *porchv1alpha1.PackageRevision {
	upstreamLock := sourcePR.Status.UpstreamLock
	repoList := configapi.RepositoryList{}
	err := r.PorchClient.List(ctx, &repoList, &client.ListOptions{})
	if err != nil {
		return nil
	}
	return r.getNewUpstreamPR(ctx, upstreamLock, &repoList, targetUpstreamRevision)
}

func (r *PackageDeploymentReconciler) updateDraft(ctx context.Context,
	sourcePR *porchv1alpha1.PackageRevision,
	newUpstreamPR *porchv1alpha1.PackageRevision) (*porchv1alpha1.PackageRevision, error) {
	oldPR := sourcePR.DeepCopy()
	tasks := sourcePR.Spec.Tasks
	cloneTask := tasks[0]
	updateTask := porchv1alpha1.Task{
		Type: porchv1alpha1.TaskTypeUpdate,
		Update: &porchv1alpha1.PackageUpdateTaskSpec{
			Upstream: cloneTask.Clone.Upstream,
		},
	}
	updateTask.Update.Upstream.UpstreamRef.Name = newUpstreamPR.Name
	oldPR.Spec.Tasks = append(oldPR.Spec.Tasks, updateTask)

	err := r.PorchClient.Update(ctx, oldPR)
	if err != nil {
		return nil, err
	}
	return oldPR, nil
}

// Updating a published package revision means making a copy of it and updating the
// newly created draft.
func (r *PackageDeploymentReconciler) updateLatest(ctx context.Context,
	sourcePR *porchv1alpha1.PackageRevision, targetPackageRevision string) (*porchv1alpha1.PackageRevision, error) {
	upstreamPR := r.discoverUpdate(ctx, sourcePR, targetPackageRevision)
	if upstreamPR == nil {
		return sourcePR, nil
	}
	newPR := &porchv1alpha1.PackageRevision{
		TypeMeta: metav1.TypeMeta{
			Kind:       "PackageRevision",
			APIVersion: porchv1alpha1.SchemeGroupVersion.Identifier(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: sourcePR.Namespace,
		},
		Spec: sourcePR.Spec,
	}

	newPR.Spec.Revision = ""
	wsNumStr := strings.TrimPrefix(string(newPR.Spec.WorkspaceName), "packagedeployment-")
	wsNum, _ := strconv.Atoi(wsNumStr)
	wsNum++
	newPR.Spec.WorkspaceName = porchv1alpha1.WorkspaceName(fmt.Sprintf("packagedeployment-%d", wsNum))
	newPR.Spec.Lifecycle = porchv1alpha1.PackageRevisionLifecycleDraft

	if err := r.PorchClient.Create(ctx, newPR); err != nil {
		r.l.Error(err, "failed to create new package revision")
		return nil, err

	}

	if err := r.loadPackageRevisions(ctx); err != nil {
		r.l.Error(err, "could not load package revisions")
		return nil, err
	}

	// get the newly created package back
	if byRepo, ok := r.packageRevs[newPR.ObjectMeta.Namespace]; ok {
		if byPkg, ok := byRepo[newPR.Spec.RepositoryName]; ok {
			if byRev, ok := byPkg[newPR.Spec.PackageName]; ok {
				drafts := byRev[""]
				for _, draft := range drafts {
					if draft.Spec.WorkspaceName == newPR.Spec.WorkspaceName {
						r.l.Info("updating the newly created draft")
						return r.updateDraft(ctx, draft, upstreamPR)
					}
				}
			}
		}
	}

	return nil, fmt.Errorf("could not find newly created package revision")
}

func (r *PackageDeploymentReconciler) getNewUpstreamPR(ctx context.Context, upstreamLock *porchv1alpha1.UpstreamLock, repositories *configapi.RepositoryList,
	targetUpstreamRevision string) *porchv1alpha1.PackageRevision {
	// This code is largely copied from the availableUpdates function in
	// this file: https://github.com/GoogleContainerTools/kpt/blob/04f18c7cd8ff97986ec75c0b1cb03cad77348320/commands/alpha/rpkg/update/discover.go#L96
	// We can probably refactor the kpt code into a library so that clients can consume it directly.
	var update *porchv1alpha1.PackageRevision

	if upstreamLock == nil || upstreamLock.Git == nil {
		return nil
	}
	var currentUpstreamRevision string

	// separate the revision number from the package name
	lastIndex := strings.LastIndex(upstreamLock.Git.Ref, "/")

	if strings.HasPrefix(upstreamLock.Git.Ref, "drafts") {
		// The upstream is not a published package, we don't support this yet.
		return nil
	}

	currentUpstreamRevision = upstreamLock.Git.Ref[lastIndex+1:]
	if currentUpstreamRevision == targetUpstreamRevision {
		// we don't need to do anything
		return nil
	}

	// upstream.git.ref looks like pkgname/version
	upstreamPackageName := upstreamLock.Git.Ref[:lastIndex]
	upstreamPackageName = strings.TrimPrefix(upstreamPackageName, "/")

	if !strings.HasSuffix(upstreamLock.Git.Repo, ".git") {
		upstreamLock.Git.Repo += ".git"
	}

	// find a repo that matches the upstreamLock
	var revisions []porchv1alpha1.PackageRevision
	for _, repo := range repositories.Items {
		if repo.Spec.Type != configapi.RepositoryTypeGit {
			// we are not currently supporting non-git repos for updates
			continue
		}
		if !strings.HasSuffix(repo.Spec.Git.Repo, ".git") {
			repo.Spec.Git.Repo += ".git"
		}
		if upstreamLock.Git.Repo == repo.Spec.Git.Repo {
			revisions, _ = r.getUpstreamRevisions(ctx, repo, upstreamPackageName)
		}
	}

	for _, upstreamRevision := range revisions {
		if upstreamRevision.Spec.Revision == targetUpstreamRevision {
			update = &upstreamRevision
			break
		}
	}

	return update
}

// fetches all package revision numbers for packages with the name upstreamPackageName from the repo
func (r *PackageDeploymentReconciler) getUpstreamRevisions(ctx context.Context, repo configapi.Repository, upstreamPackageName string) ([]porchv1alpha1.PackageRevision, error) {
	var result []porchv1alpha1.PackageRevision

	var prList porchv1alpha1.PackageRevisionList
	if err := r.PorchClient.List(ctx, &prList); err != nil {
		r.l.Error(err, "could not list package revisions")
		return nil, err
	}
	for _, pkgRev := range prList.Items {
		if pkgRev.Spec.Lifecycle != porchv1alpha1.PackageRevisionLifecyclePublished {
			// only consider published packages
			continue
		}
		if pkgRev.Spec.RepositoryName == repo.Name && pkgRev.Spec.PackageName == upstreamPackageName {
			result = append(result, pkgRev)
		}
	}
	return result, nil
}
