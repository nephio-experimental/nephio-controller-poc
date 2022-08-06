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
	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/go-logr/logr"

	automationv1alpha1 "github.com/nephio-project/nephio-controller-poc/apis/automation/v1alpha1"
	infrav1alpha1 "github.com/nephio-project/nephio-controller-poc/apis/infra/v1alpha1"

	porchv1alpha1 "github.com/GoogleContainerTools/kpt/porch/api/porch/v1alpha1"
)

// namespace->repo->package->revision
type PackageRevisionMapByRev map[string]*porchv1alpha1.PackageRevision
type PackageRevisionMapByPkg map[string]PackageRevisionMapByRev
type PackageRevisionMapByRepo map[string]PackageRevisionMapByPkg
type PackageRevisionMapByNS map[string]PackageRevisionMapByRepo

// PackageDeploymentReconciler reconciles a PackageDeployment object
type PackageDeploymentReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	PorchClient client.Client

	// NOTE: this needs to be updated with every request, it sucks
	packageRevs PackageRevisionMapByNS
	l           logr.Logger
}

//+kubebuilder:rbac:groups=automation.nephio.org,resources=packagedeployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=automation.nephio.org,resources=packagedeployments/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=automation.nephio.org,resources=packagedeployments/finalizers,verbs=update

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

	// WARNING WARNING WARNING
	// NOTE: this is bad, it's only looking at "first run" - this is PROOF OF CONCEPT
	// code, not production code. What we MUST do in a real controller is:
	//  - Identify all PackageRevisions that should exist based on the current spec
	//  - Identify existing PackageRevisions created by this controller
	//  - Prune PackageRevisions we created that no long are part of this spec
	//  - Create new PackageRevisions for new matches
	//  - Verify the version for existing matches
	//
	// This code ONLY creates new PackageRevisions for new matches
	//

	// For each cluster, we want to create a variant of the package
	// in the associated repository. There are two mutations we will
	// make to the package when rendering the variant: 1) we will inject
	// in the Namespace resource if it is not present; 2) if the package
	// contains a ClusterScaleProfile, and the cluster has an associated
	// ClusterScaleProfile, we will overwrite the package version with the
	// associated cluster version.
	//
	for _, c := range clusterList.Items {
		// Clone or update the package from upstream to the repo
		err = r.ensurePackageRevision(ctx, pd, &c, sourcePR)
		if err != nil {
			r.l.Error(err, "could not clone package")
			return ctrl.Result{}, err
		}
		// Load the contents of the new variant
		// Add a Namespace if it does not already exist in the package
		// Add set-namespace function if it doesn't already exist
		// If a ClusterScaleProfile CR exists, and one exists for the
		// associated cluster, then copy the cluster version to the package
		// Save the package
		// Propose the package
		//
	}

	return ctrl.Result{}, nil
}

func (r *PackageDeploymentReconciler) startRequest(ctx context.Context, req ctrl.Request) (*automationv1alpha1.PackageDeployment, error) {
	r.l = log.FromContext(ctx)
	r.l.Info("reconcile", "req", req)

	err := r.loadPackageRevisions(ctx)
	if err != nil {
		return nil, err
	}

	// Load the PackageDeploymen
	var pd automationv1alpha1.PackageDeployment
	if err := r.Get(ctx, req.NamespacedName, &pd); err != nil {
		r.l.Error(err, "unable to fetch PackageDeployment")
		return nil, client.IgnoreNotFound(err)
	}

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
		r.l.Info("Found", "Package", pr.Name)
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
		m[pr.Spec.RepositoryName][pr.Spec.PackageName][pr.Spec.Revision] = &pr
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
	return r.packageRevs[ns][repo][pkg][rev]
}

func (r *PackageDeploymentReconciler) ensurePackageRevision(ctx context.Context,
	pd *automationv1alpha1.PackageDeployment,
	c *infrav1alpha1.Cluster,
	sourcePR *porchv1alpha1.PackageRevision) error {

	// What we SHOULD do in here is:
	//   - Check if the target repo has the package already
	//   - If not, clone it and we're done. Otherwise continue
	//   - Compare the UPSTREAM (base) revision of the package against
	//     the package in the PackageDeployment. Note that the revision
	//     stored in r.packageRevs will contain the LOCAL (downstream)
	//     revision number of the package, so we CANNOT directly compare
	//     them.
	//   - If the PackageDeployment revision is different from the UPSTREAM
	//     revision of the package, then update the package to that revision
	//     (which could be an upgrade OR downgrade).
	//
	// What we ACTUALLY do in here is:
	//   - Just the first thing - clone it - no updates
	//
	ns := "default"
	if pd.Namespace != "" {
		ns = pd.Namespace
	}

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
			Namespace: ns,
		},
		Spec: porchv1alpha1.PackageRevisionSpec{
			PackageName:    sourcePR.Spec.PackageName,
			Revision:       sourcePR.Spec.Revision,
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
	return r.PorchClient.Create(ctx, newPR)
}

// SetupWithManager sets up the controller with the Manager.
func (r *PackageDeploymentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&automationv1alpha1.PackageDeployment{}).
		Complete(r)
}
