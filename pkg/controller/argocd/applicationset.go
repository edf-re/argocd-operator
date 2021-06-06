// Copyright 2021 ArgoCD Operator Developers
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// 	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package argocd

import (
	"context"
	"fmt"
	"os"
	"reflect"

	argoprojv1a1 "github.com/argoproj-labs/argocd-operator/pkg/apis/argoproj/v1alpha1"
	"github.com/argoproj-labs/argocd-operator/pkg/common"
	"github.com/argoproj-labs/argocd-operator/pkg/controller/argoutil"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func (r *ReconcileArgoCD) reconcileApplicationSetController(cr *argoprojv1a1.ArgoCD) error {

	log.Info("reconciling applicationset serviceaccounts")
	sa, err := r.reconcileApplicationSetServiceAccount(cr)
	if err != nil {
		return err
	}

	log.Info("reconciling applicationset roles")
	role, err := r.reconcileApplicationSetRole(cr)
	if err != nil {
		return err
	}

	log.Info("reconciling applicationset role bindings")
	if err := r.reconcileApplicationSetRoleBinding(cr, role, sa); err != nil {
		return err
	}

	log.Info("reconciling applicationset deployments")
	if err := r.reconcileApplicationSetDeployment(cr, sa); err != nil {
		return err
	}

	return nil
}

// reconcileApplicationControllerDeployment will ensure the Deployment resource is present for the ArgoCD Application Controller component.
func (r *ReconcileArgoCD) reconcileApplicationSetDeployment(cr *argoprojv1a1.ArgoCD, sa *corev1.ServiceAccount) error {
	deploy := newDeploymentWithSuffix("applicationset-controller", "controller", cr)

	setAppSetLabels(&deploy.ObjectMeta)

	podSpec := &deploy.Spec.Template.Spec

	podSpec.ServiceAccountName = sa.ObjectMeta.Name

	podSpec.Volumes = []corev1.Volume{
		{
			Name: "ssh-known-hosts",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: common.ArgoCDKnownHostsConfigMapName,
					},
				},
			},
		},
		{
			Name: "tls-certs",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: common.ArgoCDTLSCertsConfigMapName,
					},
				},
			},
		},
		{
			Name: "gpg-keys",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: common.ArgoCDGPGKeysConfigMapName,
					},
				},
			},
		},
		{
			Name: "gpg-keyring",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
		{
			Name: "argocd-repo-server-tls",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: common.ArgoCDRepoServerTLSSecretName,
					Optional:   boolPtr(true),
				},
			},
		},
	}

	podSpec.Containers = []corev1.Container{{
		Command: []string{"applicationset-controller", "--argocd-repo-server", getRepoServerAddress(cr)},
		Env: []corev1.EnvVar{{
			Name: "NAMESPACE",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "metadata.namespace",
				},
			},
		}},
		Image:           getApplicationSetContainerImage(cr),
		ImagePullPolicy: corev1.PullAlways,
		Name:            "argocd-applicationset-controller",
		Resources:       getApplicationSetResources(cr),
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "ssh-known-hosts",
				MountPath: "/app/config/ssh",
			},
			{
				Name:      "tls-certs",
				MountPath: "/app/config/tls",
			},
			{
				Name:      "gpg-keys",
				MountPath: "/app/config/gpg/source",
			},
			{
				Name:      "gpg-keyring",
				MountPath: "/app/config/gpg/keys",
			},
			{
				Name:      "argocd-repo-server-tls",
				MountPath: "/app/config/reposerver/tls",
			},
		},
	}}

	if existing := newDeploymentWithSuffix("applicationset-controller", "controller", cr); argoutil.IsObjectFound(r.client, cr.Namespace, existing.Name, existing) {

		// If the Deployment already exists, make sure the containers are up-to-date
		actualContainers := existing.Spec.Template.Spec.Containers[0]
		if !reflect.DeepEqual(actualContainers, podSpec.Containers) {
			existing.Spec.Template.Spec.Containers = podSpec.Containers
			return r.client.Update(context.TODO(), existing)
		}
		return nil // Deployment found with nothing to do, move along...
	}

	if err := controllerutil.SetControllerReference(cr, deploy, r.scheme); err != nil {
		return err
	}
	return r.client.Create(context.TODO(), deploy)

}

func (r *ReconcileArgoCD) reconcileApplicationSetServiceAccount(cr *argoprojv1a1.ArgoCD) (*corev1.ServiceAccount, error) {

	sa := newServiceAccountWithName("applicationset-controller", cr)
	setAppSetLabels(&sa.ObjectMeta)

	exists := true
	if err := argoutil.FetchObject(r.client, cr.Namespace, sa.Name, sa); err != nil {
		if !errors.IsNotFound(err) {
			return nil, err
		}
		exists = false
	}

	if exists {
		return sa, nil
	}

	if err := controllerutil.SetControllerReference(cr, sa, r.scheme); err != nil {
		return nil, err
	}

	err := r.client.Create(context.TODO(), sa)
	if err != nil {
		return nil, err
	}

	return sa, err
}

func (r *ReconcileArgoCD) reconcileApplicationSetRole(cr *argoprojv1a1.ArgoCD) (*v1.Role, error) {

	policyRules := []v1.PolicyRule{

		// ApplicationSet
		{
			APIGroups: []string{"argoproj.io"},
			Resources: []string{
				"applications",
				"applicationsets",
				"appprojects",
				"applicationsets/finalizers",
			},
			Verbs: []string{
				"create",
				"delete",
				"get",
				"list",
				"patch",
				"update",
				"watch",
			},
		},
		// ApplicationSet Status
		{
			APIGroups: []string{"argoproj.io"},
			Resources: []string{
				"applicationsets/status",
			},
			Verbs: []string{
				"get",
				"patch",
				"update",
			},
		},

		// Events
		{
			APIGroups: []string{""},
			Resources: []string{
				"events",
			},
			Verbs: []string{
				"create",
				"delete",
				"get",
				"list",
				"patch",
				"update",
				"watch",
			},
		},

		// Read Secrets/ConfigMaps
		{
			APIGroups: []string{""},
			Resources: []string{
				"secrets",
				"configmaps",
			},
			Verbs: []string{
				"get",
				"list",
				"watch",
			},
		},

		// Read Deployments
		{
			APIGroups: []string{"apps", "extensions"},
			Resources: []string{
				"deployments",
			},
			Verbs: []string{
				"get",
				"list",
				"watch",
			},
		},
	}

	role := newRole("applicationset-controller", policyRules, cr)
	setAppSetLabels(&role.ObjectMeta)

	err := r.client.Get(context.TODO(), types.NamespacedName{Name: role.Name, Namespace: cr.Namespace}, role)
	if err != nil {
		if !errors.IsNotFound(err) {
			return nil, fmt.Errorf("failed to reconcile the role for the service account associated with %s : %s", role.Name, err)
		}
		controllerutil.SetControllerReference(cr, role, r.scheme)
		return role, r.client.Create(context.TODO(), role)
	}

	role.Rules = policyRules
	controllerutil.SetControllerReference(cr, role, r.scheme)
	return role, r.client.Update(context.TODO(), role)
}

func (r *ReconcileArgoCD) reconcileApplicationSetRoleBinding(cr *argoprojv1a1.ArgoCD, role *v1.Role, sa *corev1.ServiceAccount) error {

	name := "applicationset-controller"

	// get expected name
	roleBinding := newRoleBindingWithname(name, cr)

	// fetch existing rolebinding by name
	roleBindingExists := true
	if err := r.client.Get(context.TODO(), types.NamespacedName{Name: roleBinding.Name, Namespace: cr.Namespace}, roleBinding); err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get the rolebinding associated with %s : %s", name, err)
		}
		roleBindingExists = false
	}

	setAppSetLabels(&roleBinding.ObjectMeta)

	roleBinding.RoleRef = v1.RoleRef{
		APIGroup: v1.GroupName,
		Kind:     "Role",
		Name:     role.Name,
	}

	roleBinding.Subjects = []v1.Subject{
		{
			Kind:      v1.ServiceAccountKind,
			Name:      sa.Name,
			Namespace: sa.Namespace,
		},
	}

	if err := controllerutil.SetControllerReference(cr, roleBinding, r.scheme); err != nil {
		return err
	}

	if roleBindingExists {
		return r.client.Update(context.TODO(), roleBinding)
	}

	return r.client.Create(context.TODO(), roleBinding)
}

func getApplicationSetContainerImage(cr *argoprojv1a1.ArgoCD) string {
	defaultImg, defaultTag := false, false

	img := ""
	tag := ""

	// First pull from spec, if it exists
	if cr.Spec.ApplicationSet != nil {
		img = cr.Spec.ApplicationSet.Image
		tag = cr.Spec.ApplicationSet.Version
	}

	// If spec is empty, use the defaults
	if img == "" {
		img = common.ArgoCDDefaultApplicationSetImage
		defaultImg = true
	}
	if tag == "" {
		tag = common.ArgoCDDefaultApplicationSetVersion
		defaultTag = true
	}

	// If an env var is specified then use that, but don't override the spec values (if they are present)
	if e := os.Getenv(common.ArgoCDApplicationSetEnvName); e != "" && (defaultTag && defaultImg) {
		return e
	}
	return argoutil.CombineImageTag(img, tag)
}

// getApplicationSetResources will return the ResourceRequirements for the Application Sets container.
func getApplicationSetResources(cr *argoprojv1a1.ArgoCD) corev1.ResourceRequirements {
	resources := corev1.ResourceRequirements{}

	// Allow override of resource requirements from CR
	if cr.Spec.ApplicationSet.Resources != nil {
		resources = *cr.Spec.ApplicationSet.Resources
	}

	return resources
}

func setAppSetLabels(obj *metav1.ObjectMeta) {
	obj.Labels["app.kubernetes.io/name"] = "argocd-applicationset-controller"
	obj.Labels["app.kubernetes.io/part-of"] = "argocd-applicationset"
	obj.Labels["app.kubernetes.io/component"] = "controller"
}
