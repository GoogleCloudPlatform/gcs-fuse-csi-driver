/*
Copyright 2018 The Kubernetes Authors.
Copyright 2022 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package webhook

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/admission/plugin/cel"
	"k8s.io/apiserver/pkg/admission/plugin/policy/validating"
	"k8s.io/apiserver/pkg/admission/plugin/webhook/matchconditions"
	celconfig "k8s.io/apiserver/pkg/apis/cel"
	"k8s.io/apiserver/pkg/cel/environment"
	"k8s.io/utils/ptr"
)

func testPod(initContainer bool, annotation map[string]string, restartPolicy *corev1.ContainerRestartPolicy, env []corev1.EnvVar) *corev1.Pod {
	container := corev1.Container{
		Name:          GcsFuseSidecarName,
		RestartPolicy: restartPolicy,
		Env:           env,
	}

	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: annotation,
		},
		Spec: corev1.PodSpec{},
	}

	if initContainer {
		pod.Spec.InitContainers = []corev1.Container{container}
	} else {
		pod.Spec.Containers = []corev1.Container{container}
	}

	return &pod
}

func expectedValidateResult(d1, d2 *validating.PolicyDecision) validating.ValidateResult {
	vr := validating.ValidateResult{
		Decisions:        []validating.PolicyDecision{},
		AuditAnnotations: []validating.PolicyAuditAnnotation{},
	}

	if d1 != nil {
		vr.Decisions = append(vr.Decisions, *d1)
	}

	if d2 != nil {
		vr.Decisions = append(vr.Decisions, *d2)
	}

	if d1 == nil && d2 == nil {
		vr.Decisions = nil
		vr.AuditAnnotations = nil
	}

	return vr
}

var admittedDecision = validating.PolicyDecision{
	Action:     validating.ActionAdmit,
	Evaluation: validating.EvalAdmit,
}

var missingRestartPolicyDecision = validating.PolicyDecision{
	Action:  validating.ActionDeny,
	Message: "the native gcsfuse sidecar init container must have restartPolicy:Always.",
	Reason:  metav1.StatusReasonInvalid,
}

var missingEnvVarDecision = validating.PolicyDecision{
	Action:  validating.ActionDeny,
	Message: "the native gcsfuse sidecar init container must have env var NATIVE_SIDECAR with value TRUE.",
	Reason:  metav1.StatusReasonInvalid,
}

var testCases = []struct {
	name           string
	pod            *corev1.Pod
	expectedResult validating.ValidateResult
}{
	{
		name:           "validation passed with a valid native sidecar container",
		pod:            testPod(true, map[string]string{GcsFuseVolumeEnableAnnotation: "true"}, ptr.To(corev1.ContainerRestartPolicyAlways), []corev1.EnvVar{{Name: "NATIVE_SIDECAR", Value: "TRUE"}}),
		expectedResult: expectedValidateResult(&admittedDecision, &admittedDecision),
	},
	// {
	// 	name:           "validation failed with a native sidecar container missing RestartPolicy",
	// 	pod:            testPod(true, map[string]string{GcsFuseVolumeEnableAnnotation: "true"}, nil, []corev1.EnvVar{{Name: "NATIVE_SIDECAR", Value: "TRUE"}}),
	// 	expectedResult: expectedValidateResult(&missingRestartPolicyDecision, &admittedDecision),
	// },
	// {
	// 	name:           "validation failed with a native sidecar container missing env var",
	// 	pod:            testPod(true, map[string]string{GcsFuseVolumeEnableAnnotation: "true"}, ptr.To(corev1.ContainerRestartPolicyAlways), nil),
	// 	expectedResult: expectedValidateResult(&admittedDecision, &missingEnvVarDecision),
	// },
	// {
	// 	name:           "validation failed with a valid native sidecar container missing correct env var",
	// 	pod:            testPod(true, map[string]string{GcsFuseVolumeEnableAnnotation: "true"}, ptr.To(corev1.ContainerRestartPolicyAlways), []corev1.EnvVar{{Name: "NATIVE_SIDECAR", Value: "FALSE"}}),
	// 	expectedResult: expectedValidateResult(&admittedDecision, &missingEnvVarDecision),
	// },
	// {
	// 	name:           "validation failed with a native sidecar container missing RestartPolicy and env var",
	// 	pod:            testPod(true, map[string]string{GcsFuseVolumeEnableAnnotation: "true"}, nil, nil),
	// 	expectedResult: expectedValidateResult(&missingRestartPolicyDecision, &missingEnvVarDecision),
	// },
	// {
	// 	name:           "validation skipped without pod annotations",
	// 	pod:            testPod(true, nil, nil, nil),
	// 	expectedResult: expectedValidateResult(nil, nil),
	// },
	// {
	// 	name:           "validation skipped with a pod annotation gke-gcsfuse/volumes: false",
	// 	pod:            testPod(true, map[string]string{GcsFuseVolumeEnableAnnotation: "false"}, nil, nil),
	// 	expectedResult: expectedValidateResult(nil, nil),
	// },
	// {
	// 	name:           "validation skipped with a regular sidecar container",
	// 	pod:            testPod(false, map[string]string{GcsFuseVolumeEnableAnnotation: "true"}, nil, nil),
	// 	expectedResult: expectedValidateResult(nil, nil),
	// },
}

func TestValidatingAdmissionPolicy(t *testing.T) {
	t.Parallel()

	filename := "../../deploy/base/webhook/validating_admission_policy.yaml"
	policy, err := loadValidatingAdmissionPolicy(filename)
	if err != nil {
		t.Fatalf("failed to load policy: %v", err)
	}

	validator := compilePolicy(policy)

	ignoreElapsedField := cmpopts.IgnoreFields(validating.PolicyDecision{}, "Elapsed")

	for _, tc := range testCases {
		fakeAttr := admission.NewAttributesRecord(tc.pod, nil, schema.GroupVersionKind{}, "", "", schema.GroupVersionResource{}, "", "", nil, false, nil)
		fakeVersionedAttr, _ := admission.NewVersionedAttributes(fakeAttr, schema.GroupVersionKind{}, nil)
		validateResult := validator.Validate(context.TODO(), fakeVersionedAttr.GetResource(), fakeVersionedAttr, nil, nil, celconfig.RuntimeCELCostBudget, nil)

		if diff := cmp.Diff(validateResult, tc.expectedResult, ignoreElapsedField); diff != "" {
			t.Errorf("unexpected options args (-got, +want)\n%s", diff)
		}
	}
}

func loadValidatingAdmissionPolicy(filename string) (*admissionregistrationv1.ValidatingAdmissionPolicy, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	decoder := yaml.NewYAMLToJSONDecoder(bytes.NewReader(data))

	obj := &admissionregistrationv1.ValidatingAdmissionPolicy{}
	err = decoder.Decode(obj)

	return obj, err
}

func compilePolicy(policy *admissionregistrationv1.ValidatingAdmissionPolicy) validating.Validator {
	hasParam := false
	optionalVars := cel.OptionalVariableDeclarations{HasParams: hasParam, HasAuthorizer: true, StrictCost: true}
	if policy.Spec.ParamKind != nil {
		hasParam = true
	}
	strictCost := true
	expressionOptionalVars := cel.OptionalVariableDeclarations{HasParams: hasParam, HasAuthorizer: false, StrictCost: strictCost}
	failurePolicy := policy.Spec.FailurePolicy
	var matcher matchconditions.Matcher = nil
	matchConditions := policy.Spec.MatchConditions
	compositionEnvTemplate, err := cel.NewCompositionEnv(cel.VariablesTypeName, environment.MustBaseEnvSet(environment.DefaultCompatibilityVersion(), true))
	if err != nil {
		panic(err)
	}

	filterCompiler := cel.NewCompositedCompilerFromTemplate(compositionEnvTemplate)
	filterCompiler.CompileAndStoreVariables(convertv1beta1Variables(policy.Spec.Variables), optionalVars, environment.StoredExpressions)

	if len(matchConditions) > 0 {
		matchExpressionAccessors := make([]cel.ExpressionAccessor, len(matchConditions))
		for i := range matchConditions {
			matchExpressionAccessors[i] = (*matchconditions.MatchCondition)(&matchConditions[i])
		}
		matcher = matchconditions.NewMatcher(filterCompiler.CompileCondition(matchExpressionAccessors, optionalVars, environment.StoredExpressions), failurePolicy, "policy", "validate", policy.Name)
	}
	res := validating.NewValidator(
		filterCompiler.CompileCondition(convertv1Validations(policy.Spec.Validations), optionalVars, environment.StoredExpressions),
		matcher,
		filterCompiler.CompileCondition(convertv1AuditAnnotations(policy.Spec.AuditAnnotations), optionalVars, environment.StoredExpressions),
		filterCompiler.CompileCondition(convertv1MessageExpressions(policy.Spec.Validations), expressionOptionalVars, environment.StoredExpressions),
		failurePolicy,
	)

	return res
}

func convertv1Validations(inputValidations []admissionregistrationv1.Validation) []cel.ExpressionAccessor {
	celExpressionAccessor := make([]cel.ExpressionAccessor, len(inputValidations))
	for i, validation := range inputValidations {
		validation := validating.ValidationCondition{
			Expression: validation.Expression,
			Message:    validation.Message,
			Reason:     validation.Reason,
		}
		celExpressionAccessor[i] = &validation
	}

	return celExpressionAccessor
}

func convertv1MessageExpressions(inputValidations []admissionregistrationv1.Validation) []cel.ExpressionAccessor {
	celExpressionAccessor := make([]cel.ExpressionAccessor, len(inputValidations))
	for i, validation := range inputValidations {
		if validation.MessageExpression != "" {
			condition := validating.MessageExpressionCondition{
				MessageExpression: validation.MessageExpression,
			}
			celExpressionAccessor[i] = &condition
		}
	}

	return celExpressionAccessor
}

func convertv1AuditAnnotations(inputValidations []admissionregistrationv1.AuditAnnotation) []cel.ExpressionAccessor {
	celExpressionAccessor := make([]cel.ExpressionAccessor, len(inputValidations))
	for i, validation := range inputValidations {
		validation := validating.AuditAnnotationCondition{
			Key:             validation.Key,
			ValueExpression: validation.ValueExpression,
		}
		celExpressionAccessor[i] = &validation
	}

	return celExpressionAccessor
}

func convertv1beta1Variables(variables []admissionregistrationv1.Variable) []cel.NamedExpressionAccessor {
	namedExpressions := make([]cel.NamedExpressionAccessor, len(variables))
	for i, variable := range variables {
		namedExpressions[i] = &validating.Variable{Name: variable.Name, Expression: variable.Expression}
	}

	return namedExpressions
}
