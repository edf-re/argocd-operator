package argocd

import (
	"context"
	"crypto/sha256"
	"fmt"
	"reflect"
	"sort"
	"testing"

	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"github.com/argoproj-labs/argocd-operator/pkg/apis/argoproj/v1alpha1"
	argoprojv1alpha1 "github.com/argoproj-labs/argocd-operator/pkg/apis/argoproj/v1alpha1"
)

func Test_newCASecret(t *testing.T) {
	cr := &argoprojv1alpha1.ArgoCD{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-argocd",
			Namespace: "argocd",
		},
	}

	s, err := newCASecret(cr)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		corev1.ServiceAccountRootCAKey,
		corev1.TLSCertKey,
		corev1.TLSPrivateKeyKey,
	}
	if k := byteMapKeys(s.Data); !reflect.DeepEqual(want, k) {
		t.Fatalf("got %#v, want %#v", k, want)
	}
}

func byteMapKeys(m map[string][]byte) []string {
	r := []string{}
	for k := range m {
		r = append(r, k)
	}
	sort.Strings(r)
	return r
}

func Test_ReconcileArgoCD_ReconcileRepoTLSSecret(t *testing.T) {
	argocd := &v1alpha1.ArgoCD{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argocd",
			Namespace: "argocd-operator",
			UID:       "abcd",
		},
	}
	crt := []byte("foo")
	key := []byte("bar")
	t.Run("Reconcile TLS secret", func(t *testing.T) {
		service := &v1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "argocd-repo-server",
				Namespace: "argocd-operator",
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "argoproj.io/v1alpha1",
						Kind:       "ArgoCD",
						Name:       "argocd",
						UID:        argocd.GetUID(),
					},
				},
				UID: "service-123",
			},
		}
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "argocd-repo-server-tls",
				Namespace: "argocd-operator",
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "v1",
						Kind:       "Service",
						Name:       "argocd-repo-server",
						UID:        service.GetUID(),
					},
				},
			},
			Type: corev1.SecretTypeTLS,
			Data: map[string][]byte{
				corev1.TLSCertKey:       crt,
				corev1.TLSPrivateKeyKey: key,
			},
		}
		var sumOver []byte
		sumOver = append(sumOver, crt...)
		sumOver = append(sumOver, key...)
		shasum := fmt.Sprintf("%x", sha256.Sum256(sumOver))
		serverDepl := newDeploymentWithSuffix("server", "server", argocd)
		repoDepl := newDeploymentWithSuffix("repo-server", "repo-server", argocd)
		ctrlSts := newStatefulSetWithSuffix("application-controller", "application-controller", argocd)
		objs := []runtime.Object{
			argocd,
			secret,
			service,
			serverDepl,
			repoDepl,
			ctrlSts,
		}

		r := makeReconciler(t, argocd, objs...)

		err := r.reconcileRepoServerTLSSecret(argocd)
		if err != nil {
			t.Errorf("Error should be nil, but is %v", err)
		}
		if shasum != argocd.Status.RepoTLSChecksum {
			t.Errorf("Error in SHA256 sum of secret, want=%s got=%s", shasum, argocd.Status.RepoTLSChecksum)
		}

		// Workloads should have been requested to re-rollout on a change
		r.client.Get(context.TODO(), types.NamespacedName{Name: "argocd-server", Namespace: "argocd-operator"}, serverDepl)
		deplRollout, ok := serverDepl.Spec.Template.ObjectMeta.Labels["repo.tls.cert.changed"]
		if !ok {
			t.Errorf("Expected rollout of argocd-server, but it didn't happen: %v", serverDepl.Spec.Template.ObjectMeta.Labels)
		}
		r.client.Get(context.TODO(), types.NamespacedName{Name: "argocd-repo-server", Namespace: "argocd-operator"}, repoDepl)
		repoRollout, ok := serverDepl.Spec.Template.ObjectMeta.Labels["repo.tls.cert.changed"]
		if !ok {
			t.Errorf("Expected rollout of argocd-repo-server, but it didn't happen: %v", repoDepl.Spec.Template.ObjectMeta.Labels)
		}
		r.client.Get(context.TODO(), types.NamespacedName{Name: "argocd-application-controller", Namespace: "argocd-operator"}, ctrlSts)
		ctrlRollout, ok := ctrlSts.Spec.Template.ObjectMeta.Labels["repo.tls.cert.changed"]
		if !ok {
			t.Errorf("Expected rollout of argocd-application-server, but it didn't happen: %v", ctrlSts.Spec.Template.ObjectMeta.Labels)
		}

		// Second run - no change
		err = r.reconcileRepoServerTLSSecret(argocd)
		if err != nil {
			t.Errorf("Error should be nil, but is %v", err)
		}
		if shasum != argocd.Status.RepoTLSChecksum {
			t.Errorf("Error in SHA256 sum of secret, want=%s got=%s", shasum, argocd.Status.RepoTLSChecksum)
		}

		// This time, label should not have changed
		r.client.Get(context.TODO(), types.NamespacedName{Name: "argocd-server", Namespace: "argocd-operator"}, serverDepl)
		deplRolloutNew, ok := serverDepl.Spec.Template.ObjectMeta.Labels["repo.tls.cert.changed"]
		if !ok || deplRollout != deplRolloutNew {
			t.Errorf("Did not expect rollout of argocd-server, but it did happen: %v", serverDepl.Spec.Template.ObjectMeta.Labels)
		}
		r.client.Get(context.TODO(), types.NamespacedName{Name: "argocd-repo-server", Namespace: "argocd-operator"}, repoDepl)
		repoRolloutNew, ok := serverDepl.Spec.Template.ObjectMeta.Labels["repo.tls.cert.changed"]
		if !ok || repoRollout != repoRolloutNew {
			t.Errorf("Did not expect rollout of argocd-repo-server, but it did happen: %v", serverDepl.Spec.Template.ObjectMeta.Labels)
		}
		r.client.Get(context.TODO(), types.NamespacedName{Name: "argocd-application-controller", Namespace: "argocd-operator"}, ctrlSts)
		ctrlRolloutNew, ok := ctrlSts.Spec.Template.ObjectMeta.Labels["repo.tls.cert.changed"]
		if !ok || ctrlRollout != ctrlRolloutNew {
			t.Errorf("Did not expect rollout of argocd-application-server, but it did happen: %v", serverDepl.Spec.Template.ObjectMeta.Labels)
		}

		// Update certificate in the secret must trigger new rollout
		r.client.Get(context.TODO(), types.NamespacedName{Name: "argocd-repo-server-tls", Namespace: "argocd-operator"}, secret)
		secret.Data["tls.crt"] = []byte("bar")
		r.client.Update(context.TODO(), secret)

		sumOver = []byte{}
		sumOver = append(sumOver, []byte("bar")...)
		sumOver = append(sumOver, key...)
		shasum = fmt.Sprintf("%x", sha256.Sum256(sumOver))

		// Second run - no change
		err = r.reconcileRepoServerTLSSecret(argocd)
		if err != nil {
			t.Errorf("Error should be nil, but is %v", err)
		}
		if shasum != argocd.Status.RepoTLSChecksum {
			t.Errorf("Error in SHA256 sum of secret, want=%s got=%s", shasum, argocd.Status.RepoTLSChecksum)
		}

		// This time, label should not have changed
		r.client.Get(context.TODO(), types.NamespacedName{Name: "argocd-server", Namespace: "argocd-operator"}, serverDepl)
		deplRolloutNew, ok = serverDepl.Spec.Template.ObjectMeta.Labels["repo.tls.cert.changed"]
		if !ok || deplRollout == deplRolloutNew {
			t.Errorf("Expected rollout of argocd-server, but it didn't happen: %v", serverDepl.Spec.Template.ObjectMeta.Labels)
		}
		r.client.Get(context.TODO(), types.NamespacedName{Name: "argocd-repo-server", Namespace: "argocd-operator"}, repoDepl)
		repoRolloutNew, ok = serverDepl.Spec.Template.ObjectMeta.Labels["repo.tls.cert.changed"]
		if !ok || repoRollout == repoRolloutNew {
			t.Errorf("Expected rollout of argocd-repo-server, but it didn't happen: %v", repoDepl.Spec.Template.ObjectMeta.Labels)
		}
		r.client.Get(context.TODO(), types.NamespacedName{Name: "argocd-application-controller", Namespace: "argocd-operator"}, ctrlSts)
		ctrlRolloutNew, ok = ctrlSts.Spec.Template.ObjectMeta.Labels["repo.tls.cert.changed"]
		if !ok || ctrlRollout == ctrlRolloutNew {
			t.Errorf("Expected rollout of argocd-application-controller, but it didn't happen: %v", ctrlSts.Spec.Template.ObjectMeta.Labels)
		}

	})

}
