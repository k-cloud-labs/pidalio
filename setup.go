package pidalio

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/k-cloud-labs/pidalio/pkg/lister"
	policyv1alpha1 "github.com/k-cloud-labs/pkg/apis/policy/v1alpha1"
	"github.com/k-cloud-labs/pkg/client/clientset/versioned"
	clientsetscheme "github.com/k-cloud-labs/pkg/client/clientset/versioned/scheme"
	"github.com/k-cloud-labs/pkg/client/listers/policy/v1alpha1"
	"github.com/k-cloud-labs/pkg/utils/dynamiclister"
	"github.com/k-cloud-labs/pkg/utils/informermanager"
	"github.com/k-cloud-labs/pkg/utils/interrupter"
	"github.com/k-cloud-labs/pkg/utils/metrics"
	"github.com/k-cloud-labs/pkg/utils/overridemanager"
	"github.com/k-cloud-labs/pkg/utils/templatemanager"
	"github.com/k-cloud-labs/pkg/utils/templatemanager/templates"
	"github.com/k-cloud-labs/pkg/utils/tokenmanager"
)

// aggregatedScheme aggregates Kubernetes and extended schemes.
var aggregatedScheme = runtime.NewScheme()

func init() {
	var _ = scheme.AddToScheme(aggregatedScheme) // add Kubernetes schemes
	// add custom crd scheme to aggregatedScheme
	var _ = clientsetscheme.AddToScheme(aggregatedScheme)
}

// NewForConfig creates a new client for the given config.
func NewForConfig(config *rest.Config) (client.Client, error) {
	return client.New(config, client.Options{
		Scheme: aggregatedScheme,
	})
}

type setupManager struct {
	policyClient             versioned.Interface
	client                   client.Client
	drLister                 dynamiclister.DynamicResourceLister
	opLister                 v1alpha1.OverridePolicyLister
	copLister                v1alpha1.ClusterOverridePolicyLister
	informerManager          informermanager.SingleClusterInformerManager
	overrideManager          overridemanager.OverrideManager
	policyInterrupterManager interrupter.PolicyInterrupterManager
	tokenManager             tokenmanager.TokenManager
}

func (s *setupManager) setupAll(cfg *rest.Config, done <-chan struct{}) error {
	if err := s.init(cfg, done); err != nil {
		return err
	}

	if err := s.setupOverridePolicyManager(); err != nil {
		return err
	}

	if err := s.setupInterrupter(); err != nil {
		return err
	}

	return nil
}

func (s *setupManager) init(cfg *rest.Config, done <-chan struct{}) error {
	cli, err := NewForConfig(cfg)
	if err != nil {
		return err
	}

	pc, err := versioned.NewForConfig(cfg)
	if err != nil {
		return err
	}

	s.policyClient = pc
	s.client = cli
	s.informerManager = informermanager.NewSingleClusterInformerManager(dynamic.NewForConfigOrDie(cfg), 0, done)
	s.policyInterrupterManager = interrupter.NewPolicyInterrupterManager()
	s.tokenManager = tokenmanager.NewTokenManager()

	s.drLister, err = dynamiclister.NewDynamicResourceLister(cfg, done)
	if err != nil {
		klog.ErrorS(err, "failed to init dynamic client.")
		return err
	}

	return nil
}

func (s *setupManager) waitForCacheSync() error {
	s.informerManager.Start()
	if result := s.informerManager.WaitForCacheSync(); !result[opGVR] || !result[copGVR] {
		return errors.New("failed to sync override policy")
	}

	return nil
}

func (s *setupManager) setupInterrupter() error {
	otm, err := templatemanager.NewOverrideTemplateManager(&templatemanager.TemplateSource{
		Content:      templates.OverrideTemplate,
		TemplateName: "BaseTemplate",
	})
	if err != nil {
		klog.ErrorS(err, "failed to setup mutating template manager.")
		return err
	}

	vtm, err := templatemanager.NewValidateTemplateManager(&templatemanager.TemplateSource{
		Content:      templates.ValidateTemplate,
		TemplateName: "BaseTemplate",
	})
	if err != nil {
		klog.ErrorS(err, "failed to setup validate template manager.")
		return err
	}

	// base
	baseInterrupter := interrupter.NewBaseInterrupter(otm, vtm, templatemanager.NewCueManager())

	// op
	overridePolicyInterrupter := interrupter.NewOverridePolicyInterrupter(baseInterrupter, s.tokenManager, s.client, s.opLister)
	s.policyInterrupterManager.AddInterrupter(schema.GroupVersionKind{
		Group:   policyv1alpha1.SchemeGroupVersion.Group,
		Version: policyv1alpha1.SchemeGroupVersion.Version,
		Kind:    "OverridePolicy",
	}, overridePolicyInterrupter)
	// cop
	s.policyInterrupterManager.AddInterrupter(schema.GroupVersionKind{
		Group:   policyv1alpha1.SchemeGroupVersion.Group,
		Version: policyv1alpha1.SchemeGroupVersion.Version,
		Kind:    "ClusterOverridePolicy",
	}, interrupter.NewClusterOverridePolicyInterrupter(overridePolicyInterrupter, s.copLister))

	return s.policyInterrupterManager.OnStartUp()
}

var (
	opGVR = schema.GroupVersionResource{
		Group:    policyv1alpha1.SchemeGroupVersion.Group,
		Version:  policyv1alpha1.SchemeGroupVersion.Version,
		Resource: "overridepolicies",
	}
	copGVR = schema.GroupVersionResource{
		Group:    policyv1alpha1.SchemeGroupVersion.Group,
		Version:  policyv1alpha1.SchemeGroupVersion.Version,
		Resource: "clusteroverridepolicies",
	}
)

func (s *setupManager) setupOverridePolicyManager() (err error) {
	opInformer := s.informerManager.Informer(opGVR)
	copInformer := s.informerManager.Informer(copGVR)

	opInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			metrics.IncrPolicy("OverridePolicy")
			_ = s.onAddOverridePolicyPolicy(obj)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			_ = s.onUpdaterOverridePolicyPolicy(oldObj, newObj)
		},
		DeleteFunc: func(obj interface{}) {
			metrics.DecPolicy("OverridePolicy")
		},
	})

	copInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			metrics.IncrPolicy("ClusterOverridePolicy")
			_ = s.onAddClusterOverridePolicy(obj)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			_ = s.onUpdateClusterOverridePolicy(oldObj, newObj)
		},
		DeleteFunc: func(obj interface{}) {
			metrics.DecPolicy("ClusterOverridePolicy")
		},
	})

	s.opLister = lister.NewUnstructuredOverridePolicyLister(opInformer.GetIndexer())
	s.copLister = lister.NewUnstructuredClusterOverridePolicyLister(copInformer.GetIndexer())
	s.overrideManager = overridemanager.NewOverrideManager(s.drLister, s.copLister, s.opLister)
	return nil
}

const (
	lastSyncTimeAnno = "policy.kcloudlabs.io/last-sync-time"
)

func (s *setupManager) onAddOverridePolicyPolicy(obj any) error {
	op := new(policyv1alpha1.OverridePolicy)
	if err := convertToPolicy(obj.(*unstructured.Unstructured), op); err != nil {
		return err
	}

	op.Annotations[lastSyncTimeAnno] = strconv.FormatInt(time.Now().UnixNano(), 10)
	_, err := s.policyClient.PolicyV1alpha1().OverridePolicies(op.GetNamespace()).Update(context.TODO(), op, metav1.UpdateOptions{})

	return err
}

func (s *setupManager) onUpdaterOverridePolicyPolicy(oldObj, newObj any) error {
	old := new(policyv1alpha1.OverridePolicy)
	if err := convertToPolicy(oldObj.(*unstructured.Unstructured), old); err != nil {
		return err
	}

	current := new(policyv1alpha1.OverridePolicy)
	if err := convertToPolicy(newObj.(*unstructured.Unstructured), current); err != nil {
		return err
	}

	if reflect.DeepEqual(current.Spec, old.Spec) {
		return nil
	}

	current.Annotations[lastSyncTimeAnno] = strconv.FormatInt(time.Now().UnixNano(), 10)
	_, err := s.policyClient.PolicyV1alpha1().OverridePolicies(current.GetNamespace()).Update(context.TODO(), current, metav1.UpdateOptions{})
	return err
}

func (s *setupManager) onAddClusterOverridePolicy(obj any) error {
	cop := new(policyv1alpha1.ClusterOverridePolicy)
	if err := convertToPolicy(obj.(*unstructured.Unstructured), cop); err != nil {
		return err
	}

	cop.Annotations[lastSyncTimeAnno] = strconv.FormatInt(time.Now().UnixNano(), 10)
	_, err := s.policyClient.PolicyV1alpha1().ClusterOverridePolicies().Update(context.TODO(), cop, metav1.UpdateOptions{})

	return err
}

func (s *setupManager) onUpdateClusterOverridePolicy(oldObj, newObj any) error {
	old := new(policyv1alpha1.ClusterOverridePolicy)
	if err := convertToPolicy(oldObj.(*unstructured.Unstructured), old); err != nil {
		return err
	}

	current := new(policyv1alpha1.ClusterOverridePolicy)
	if err := convertToPolicy(newObj.(*unstructured.Unstructured), current); err != nil {
		return err
	}

	if reflect.DeepEqual(current.Spec, old.Spec) {
		return nil
	}

	current.Annotations[lastSyncTimeAnno] = strconv.FormatInt(time.Now().UnixNano(), 10)
	_, err := s.policyClient.PolicyV1alpha1().ClusterOverridePolicies().Update(context.TODO(), current, metav1.UpdateOptions{})
	return err
}

func convertToPolicy(u *unstructured.Unstructured, data any) error {
	klog.V(4).Infof("convertToPolicy, obj=%v", u)
	b, err := u.MarshalJSON()
	if err != nil {
		return err
	}

	return json.Unmarshal(b, data)
}
