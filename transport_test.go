package pidalio

import (
	"reflect"
	"testing"

	"github.com/golang/mock/gomock"
	jsonpatchv2 "gomodules.xyz/jsonpatch/v2"
	admissionv1 "k8s.io/api/admission/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/k-cloud-labs/pkg/utils"
	"github.com/k-cloud-labs/pkg/utils/overridemanager"
	"github.com/k-cloud-labs/pkg/utils/util"

	policyv1alpha1 "github.com/k-cloud-labs/pkg/apis/policy/v1alpha1"
	v1alpha10 "github.com/k-cloud-labs/pkg/client/listers/policy/v1alpha1"
	"github.com/k-cloud-labs/pkg/test/helper"
	"github.com/k-cloud-labs/pkg/test/mock"
)

func TestPolicyTransport_RoundTrip(t *testing.T) {
	deployment := helper.NewDeployment(metav1.NamespaceDefault, "test")
	deploymentObj, _ := util.ToUnstructured(deployment)

	overriders1 := policyv1alpha1.Overriders{
		Plaintext: []policyv1alpha1.PlaintextOverrider{
			{
				Path:     "/metadata/annotations",
				Operator: "add",
				Value:    apiextensionsv1.JSON{Raw: []byte("{\"foo\": \"bar\"}")},
			},
		},
	}
	overriders2 := policyv1alpha1.Overriders{
		Plaintext: []policyv1alpha1.PlaintextOverrider{
			{
				Path:     "/metadata/annotations",
				Operator: "add",
				Value:    apiextensionsv1.JSON{Raw: []byte("{\"hello\": \"world\"}")},
			},
		},
	}

	overridePolicy1 := &policyv1alpha1.OverridePolicy{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: metav1.NamespaceDefault,
			Name:      "overridePolicy1",
		},
		Spec: policyv1alpha1.OverridePolicySpec{
			ResourceSelectors: []policyv1alpha1.ResourceSelector{
				{
					APIVersion: deployment.APIVersion,
					Kind:       deployment.Kind,
					Name:       deployment.Name,
				},
			},
			OverrideRules: []policyv1alpha1.RuleWithOperation{
				{
					TargetOperations: []admissionv1.Operation{admissionv1.Create},
					Overriders:       overriders1,
				},
			},
		},
	}
	overridePolicy2 := &policyv1alpha1.ClusterOverridePolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name: "overridePolicy2",
		},
		Spec: policyv1alpha1.OverridePolicySpec{
			OverrideRules: []policyv1alpha1.RuleWithOperation{
				{
					TargetOperations: []admissionv1.Operation{admissionv1.Create, admissionv1.Update},
					Overriders:       overriders2,
				},
			},
		},
	}

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	copLister := mock.NewMockClusterOverridePolicyLister(ctrl)
	opLister := mock.NewMockOverridePolicyLister(ctrl)

	opLister.EXPECT().List(labels.Everything()).Return([]*policyv1alpha1.OverridePolicy{
		overridePolicy1,
	}, nil).AnyTimes()

	copLister.EXPECT().List(labels.Everything()).Return([]*policyv1alpha1.ClusterOverridePolicy{
		overridePolicy2,
	}, nil).AnyTimes()

	manager := overridemanager.NewOverrideManager(nil, copLister, opLister)

	tests := []struct {
		name              string
		opLister          v1alpha10.OverridePolicyLister
		copLister         v1alpha10.ClusterOverridePolicyLister
		resource          *unstructured.Unstructured
		operation         admissionv1.Operation
		wantedAnnotations map[string]string
		wantedErr         error
	}{
		{
			name:      "OverrideRules test 1",
			opLister:  opLister,
			copLister: copLister,
			resource:  deploymentObj,
			operation: admissionv1.Create,
			wantedErr: nil,
			wantedAnnotations: map[string]string{
				"foo":                         "bar",
				utils.AppliedOverrides:        `[{"policyName":"overridePolicy1","overriders":{"plaintext":[{"path":"/metadata/annotations","op":"add","value":{"foo":"bar"}}]}}]`,
				utils.AppliedClusterOverrides: `[{"policyName":"overridePolicy2","overriders":{"plaintext":[{"path":"/metadata/annotations","op":"add","value":{"hello":"world"}}]}}]`,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ApplyOverridePolicy(manager, tt.resource, tt.operation); !reflect.DeepEqual(err, tt.wantedErr) {
				t.Errorf("ApplyOverridePolicy() = %v, want %v", err, tt.wantedErr)
			}
			if !reflect.DeepEqual(deploymentObj.GetAnnotations(), tt.wantedAnnotations) {
				t.Errorf("ApplyOverridePolicy() Annotation = %v, want %v", deploymentObj.GetAnnotations(), tt.wantedAnnotations)
			}
		})
	}
}

func Test_applyJSONPatch(t *testing.T) {
	type args struct {
		objStr    string
		overrides []jsonpatchv2.JsonPatchOperation
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name: "1",
			args: args{
				objStr: `
{
"apiVersion": "v1",
"kind": "Pod",
"metadata":{
	"name":"web-1",
	"annotations": {
		"owner": "pidalio"
}
}
}
`,
				overrides: []jsonpatchv2.JsonPatchOperation{
					{
						Operation: "add",
						Path:      "/metadata/annotations/hello",
						Value:     "world",
					},
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := new(unstructured.Unstructured)
			if err := obj.UnmarshalJSON([]byte(tt.args.objStr)); err != nil {
				t.Errorf("unmarshal error = %v", err)
				return
			}
			if err := applyJSONPatch(obj, tt.args.overrides); (err != nil) != tt.wantErr {
				t.Errorf("applyJSONPatch() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_bytesToUnstructured(t *testing.T) {
	type args struct {
		bytes []byte
	}
	tests := []struct {
		name    string
		args    args
		want    *unstructured.Unstructured
		wantErr bool
	}{
		{
			name: "1",
			args: args{
				bytes: []byte(`
{
"apiVersion": "v1",
"kind": "Pod",
"metadata":{
	"name":"web-1",
	"annotations": {
		"owner": "pidalio"
}
}
}
`),
			},
			want: &unstructured.Unstructured{Object: map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "Pod",
				"metadata": map[string]interface{}{
					"name": "web-1",
					"annotations": map[string]interface{}{
						"owner": "pidalio",
					},
				},
			}},
			wantErr: false,
		},
		{
			name:    "err",
			args:    args{},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := bytesToUnstructured(tt.args.bytes)
			if (err != nil) != tt.wantErr {
				t.Errorf("bytesToUnstructured() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("bytesToUnstructured() got = %v, want %v", got, tt.want)
			}
		})
	}
}
