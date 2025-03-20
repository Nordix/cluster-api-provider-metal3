/*
Copyright 2019 The Kubernetes Authors.
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

package webhooks

import (
	"testing"
	"time"

	infrav1 "github.com/metal3-io/cluster-api-provider-metal3/api/v1beta1"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMetal3RemediationTemplateDefault(t *testing.T) {
	g := NewWithT(t)
	m3rt := &infrav1.Metal3RemediationTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-m3r",
			Namespace: "fooboo",
		},
		Spec: infrav1.Metal3RemediationTemplateSpec{
			Template: infrav1.Metal3RemediationTemplateResource{
				Spec: infrav1.Metal3RemediationSpec{
					Strategy: &infrav1.RemediationStrategy{
						Type:       "",
						RetryLimit: -1,
					},
				},
			},
		},
	}
	webhook := &Metal3RemediationTemplate{}

	_ = webhook.Default(ctx, m3rt)
	g.Expect(m3rt.Spec.Template.Spec.Strategy.Type).ToNot(BeNil())
	g.Expect(m3rt.Spec.Template.Spec.Strategy.Type).To(Equal(infrav1.RebootRemediationStrategy))
	g.Expect(m3rt.Spec.Template.Spec.Strategy.RetryLimit).ToNot(BeNil())
	g.Expect(m3rt.Spec.Template.Spec.Strategy.RetryLimit).To(Equal(1))
	g.Expect(m3rt.Spec.Template.Spec.Strategy.Timeout).ToNot(BeNil())
	g.Expect(*m3rt.Spec.Template.Spec.Strategy.Timeout).To(Equal(metav1.Duration{Duration: 600 * time.Second}))

	m3rt = &infrav1.Metal3RemediationTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-m3r",
			Namespace: "fooboo",
		},
		Spec: infrav1.Metal3RemediationTemplateSpec{
			Template: infrav1.Metal3RemediationTemplateResource{
				Spec: infrav1.Metal3RemediationSpec{
					Strategy: &infrav1.RemediationStrategy{
						RetryLimit: 0,
					},
				},
			},
		},
	}

	_ = webhook.Default(ctx, m3rt)
	g.Expect(m3rt.Spec.Template.Spec.Strategy.RetryLimit).ToNot(BeNil())
	g.Expect(m3rt.Spec.Template.Spec.Strategy.RetryLimit).To(Equal(1))
}

func TestMetal3RemediationTemplateValidation(t *testing.T) {
	zeroSeconds := metav1.Duration{Duration: 0}
	thirtySeconds := metav1.Duration{Duration: 30 * time.Second}
	threeMinutes := metav1.Duration{Duration: 3 * time.Minute}
	minusDuration := metav1.Duration{Duration: -1 * time.Minute}

	const WrongRemediationStrategy infrav1.RemediationType = "foo"

	tests := []struct {
		name      string
		timeout   *metav1.Duration
		limit     int
		strategy  infrav1.RemediationType
		expectErr bool
	}{
		{
			name:      "when the Timeout is not given",
			timeout:   nil,
			limit:     1,
			strategy:  infrav1.RebootRemediationStrategy,
			expectErr: false,
		},
		{
			name:      "when the Timeout is greater than 100s",
			timeout:   &threeMinutes,
			limit:     1,
			strategy:  infrav1.RebootRemediationStrategy,
			expectErr: false,
		},
		{
			name:      "when the Timeout is less than 100s",
			timeout:   &thirtySeconds,
			limit:     1,
			strategy:  infrav1.RebootRemediationStrategy,
			expectErr: true,
		},
		{
			name:      "when the Timeout is less than 0",
			timeout:   &minusDuration,
			limit:     1,
			strategy:  infrav1.RebootRemediationStrategy,
			expectErr: true,
		},
		{
			name:      "when the Timeout is 0",
			timeout:   &zeroSeconds,
			limit:     1,
			strategy:  infrav1.RebootRemediationStrategy,
			expectErr: true,
		},
		{
			name:      "when the Remediation Type is Reboot",
			timeout:   &threeMinutes,
			limit:     1,
			strategy:  infrav1.RebootRemediationStrategy,
			expectErr: false,
		},
		{
			name:      "when the Remediation Type is not Reboot",
			timeout:   &threeMinutes,
			limit:     1,
			strategy:  WrongRemediationStrategy,
			expectErr: true,
		},
		{
			name:      "when the RetryLimit is less than minRetryLimit",
			timeout:   &threeMinutes,
			limit:     0,
			strategy:  infrav1.RebootRemediationStrategy,
			expectErr: true,
		},
		{
			name:      "when the RetryLimit is minRetryLimit",
			timeout:   &threeMinutes,
			limit:     1,
			strategy:  infrav1.RebootRemediationStrategy,
			expectErr: false,
		},
		{
			name:      "when the RetryLimit is greater than minRetryLimit",
			timeout:   &threeMinutes,
			limit:     3,
			strategy:  infrav1.RebootRemediationStrategy,
			expectErr: false,
		},
	}

	for _, tt := range tests {
		g := NewWithT(t)
		webhook := &Metal3RemediationTemplate{}

		m3rt := &infrav1.Metal3RemediationTemplate{
			Spec: infrav1.Metal3RemediationTemplateSpec{
				Template: infrav1.Metal3RemediationTemplateResource{
					Spec: infrav1.Metal3RemediationSpec{
						Strategy: &infrav1.RemediationStrategy{
							Timeout:    tt.timeout,
							RetryLimit: tt.limit,
							Type:       tt.strategy,
						},
					},
				},
			},
		}

		if tt.expectErr {
			_, err := webhook.ValidateCreate(ctx, m3rt)
			g.Expect(err).To(HaveOccurred())
			_, err = webhook.ValidateUpdate(ctx, nil, m3rt)
			g.Expect(err).To(HaveOccurred())
		} else {
			_, err := webhook.ValidateCreate(ctx, m3rt)
			g.Expect(err).NotTo(HaveOccurred())
			_, err = webhook.ValidateUpdate(ctx, nil, m3rt)
			g.Expect(err).NotTo(HaveOccurred())
		}
	}
}
