// Copyright 2022 Antrea Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package labelidentity

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	"antrea.io/antrea/pkg/controller/types"
)

var (
	pSelWeb = &metav1.LabelSelector{
		MatchLabels: map[string]string{"app": "web"},
	}
	pSelDB = &metav1.LabelSelector{
		MatchLabels: map[string]string{"app": "db"},
	}
	nsSelTest = &metav1.LabelSelector{
		MatchLabels: map[string]string{"purpose": "test"},
	}
	selectorItemA = &selectorItem{
		selector: types.NewGroupSelector("", pSelWeb, nil, nil, nil),
	}
	selectorItemB = &selectorItem{
		selector: types.NewGroupSelector("", pSelWeb, nsSelTest, nil, nil),
	}
	selectorItemC = &selectorItem{
		selector: types.NewGroupSelector("", pSelDB, nil, nil, nil),
	}
	selectorItemD = &selectorItem{
		selector: types.NewGroupSelector("testing", pSelDB, nil, nil, nil),
	}
	selectorItemE = &selectorItem{
		selector: types.NewGroupSelector("random", pSelDB, nil, nil, nil),
	}
	selectorItemACopy = &selectorItem{
		selector:          types.NewGroupSelector("", pSelWeb, nil, nil, nil),
		policyKeys:        sets.NewString("policyA"),
		labelIdentityKeys: sets.NewString(),
	}
	selectorItemBCopy = &selectorItem{
		selector:          types.NewGroupSelector("", pSelWeb, nsSelTest, nil, nil),
		policyKeys:        sets.NewString("policyB"),
		labelIdentityKeys: sets.NewString(),
	}
	selectorItemCCopy = &selectorItem{
		selector:          types.NewGroupSelector("", pSelDB, nil, nil, nil),
		policyKeys:        sets.NewString("policyA", "policyB"),
		labelIdentityKeys: sets.NewString(),
	}
	selectorItemDCopy = &selectorItem{
		selector:          types.NewGroupSelector("testing", pSelDB, nil, nil, nil),
		policyKeys:        sets.NewString("policyD"),
		labelIdentityKeys: sets.NewString(),
	}
	selectorItemECopy = &selectorItem{
		selector:          types.NewGroupSelector("random", pSelDB, nil, nil, nil),
		policyKeys:        sets.NewString("policyE"),
		labelIdentityKeys: sets.NewString(),
	}
	labelA      = "ns:kubernetes.io/metadata.name=testing,purpose=test&pod:app=web"
	labelB      = "ns:kubernetes.io/metadata.name=testing,purpose=test&pod:app=db"
	labelC      = "ns:kubernetes.io/metadata.name=nomatch,purpose=nomatch&pod:app=db"
	labelMatchA = newLabelIdentityMatch(labelA, 1)
	labelMatchB = newLabelIdentityMatch(labelB, 2)
	labelMatchC = newLabelIdentityMatch(labelC, 3)
)

func TestLabelIdentityMatch(t *testing.T) {
	tests := []struct {
		label       string
		labelMatch  *labelIdentityMatch
		selector    *selectorItem
		expectMatch bool
	}{
		{
			label:       labelA,
			labelMatch:  labelMatchA,
			selector:    selectorItemA,
			expectMatch: true,
		},
		{
			label:       labelA,
			labelMatch:  labelMatchA,
			selector:    selectorItemB,
			expectMatch: true,
		},
		{
			label:       labelA,
			labelMatch:  labelMatchA,
			selector:    selectorItemC,
			expectMatch: false,
		},
		{
			label:       labelA,
			labelMatch:  labelMatchA,
			selector:    selectorItemD,
			expectMatch: false,
		},
		{
			label:       labelB,
			labelMatch:  labelMatchB,
			selector:    selectorItemB,
			expectMatch: false,
		},
		{
			label:       labelB,
			labelMatch:  labelMatchB,
			selector:    selectorItemC,
			expectMatch: true,
		},
		{
			label:       labelB,
			labelMatch:  labelMatchB,
			selector:    selectorItemD,
			expectMatch: true,
		},
		{
			label:       labelB,
			labelMatch:  labelMatchB,
			selector:    selectorItemE,
			expectMatch: false,
		},
		{
			label:       labelC,
			labelMatch:  labelMatchC,
			selector:    selectorItemB,
			expectMatch: false,
		},
		{
			label:       labelC,
			labelMatch:  labelMatchC,
			selector:    selectorItemC,
			expectMatch: true,
		},
		{
			label:       labelC,
			labelMatch:  labelMatchC,
			selector:    selectorItemD,
			expectMatch: false,
		},
		{
			label:       labelC,
			labelMatch:  labelMatchC,
			selector:    selectorItemE,
			expectMatch: false,
		},
	}
	for _, tt := range tests {
		matched := tt.labelMatch.matches(tt.selector)
		if tt.expectMatch != matched {
			t.Errorf("Unexpected matching status for %s and %s. Exp: %v, Act: %v", tt.label, tt.selector.getKey(), tt.expectMatch, matched)
		}
	}
}

func TestAddSelector(t *testing.T) {
	tests := []struct {
		name                 string
		selectorToAdd        *types.GroupSelector
		policyKey            string
		expMatchedIDs        []uint32
		expLabelIdentityKeys sets.String
		expPolicyKeys        sets.String
	}{
		{
			name:                 "cluster-wide app=web",
			selectorToAdd:        types.NewGroupSelector("", pSelWeb, nil, nil, nil),
			policyKey:            "policyA",
			expMatchedIDs:        []uint32{1},
			expLabelIdentityKeys: sets.NewString(labelA),
			expPolicyKeys:        sets.NewString("policyA"),
		},
		{
			name:                 "cluster-wide app=web another policy",
			selectorToAdd:        types.NewGroupSelector("", pSelWeb, nil, nil, nil),
			policyKey:            "policyB",
			expMatchedIDs:        []uint32{1},
			expLabelIdentityKeys: sets.NewString(labelA),
			expPolicyKeys:        sets.NewString("policyA", "policyB"),
		},
		{
			name:                 "pod app=web and ns purpose=test",
			selectorToAdd:        types.NewGroupSelector("", pSelWeb, nsSelTest, nil, nil),
			policyKey:            "policyB",
			expMatchedIDs:        []uint32{1},
			expLabelIdentityKeys: sets.NewString(labelA),
			expPolicyKeys:        sets.NewString("policyB"),
		},
		{
			name:                 "cluster-wide app=db",
			selectorToAdd:        types.NewGroupSelector("", pSelDB, nil, nil, nil),
			policyKey:            "policyC",
			expMatchedIDs:        []uint32{2, 3},
			expLabelIdentityKeys: sets.NewString(labelB, labelC),
			expPolicyKeys:        sets.NewString("policyC"),
		},
		{
			name:                 "app=db in ns testing",
			selectorToAdd:        types.NewGroupSelector("testing", pSelDB, nil, nil, nil),
			policyKey:            "policyD",
			expMatchedIDs:        []uint32{2},
			expLabelIdentityKeys: sets.NewString(labelB),
			expPolicyKeys:        sets.NewString("policyD"),
		},
		{
			name:                 "app=db in ns random",
			selectorToAdd:        types.NewGroupSelector("random", pSelDB, nil, nil, nil),
			policyKey:            "policyE",
			expMatchedIDs:        []uint32{},
			expLabelIdentityKeys: sets.NewString(),
			expPolicyKeys:        sets.NewString("policyE"),
		},
	}
	i := NewLabelIdentityIndex()
	i.labelIdentities = map[string]*labelIdentityMatch{
		labelA: labelMatchA,
		labelB: labelMatchB,
		labelC: labelMatchC,
	}
	i.labelIdentityNamespaceIndex = map[string]sets.String{
		"testing": sets.NewString(labelA, labelB),
		"nomatch": sets.NewString(labelC),
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idsMatched := i.AddSelector(tt.selectorToAdd, tt.policyKey)
			assert.ElementsMatch(t, tt.expMatchedIDs, idsMatched)
			s, exists, _ := i.selectorItems.GetByKey(tt.selectorToAdd.NormalizedName)
			if !exists {
				t.Errorf("Failed to add selector %s to the LabelIdentityIndex", tt.name)
			}
			sItem := s.(*selectorItem)
			if !tt.expLabelIdentityKeys.Equal(sItem.labelIdentityKeys) {
				t.Errorf("Unexpected label identity keys for selectorItem %s. Exp: %v, Act: %v", tt.name, tt.expLabelIdentityKeys, sItem.labelIdentityKeys)
			}
			if !tt.expPolicyKeys.Equal(sItem.policyKeys) {
				t.Errorf("Unexpected policy keys for selectorItem %s. Exp: %v, Act: %v", tt.name, tt.expPolicyKeys, sItem.policyKeys)
			}
		})
	}
}

func TestDeletePolicySelectors(t *testing.T) {
	tests := []struct {
		policyKey     string
		staleSelector string
	}{
		{
			policyKey:     "policyA",
			staleSelector: selectorItemD.getKey(),
		},
		{
			policyKey:     "policyB",
			staleSelector: selectorItemC.getKey(),
		},
	}
	i := NewLabelIdentityIndex()
	labelMatchB.selectorItemKeys = sets.NewString(selectorItemC.getKey(), selectorItemD.getKey())
	labelMatchC.selectorItemKeys = sets.NewString(selectorItemC.getKey())
	i.labelIdentities = map[string]*labelIdentityMatch{
		labelB: labelMatchB,
		labelC: labelMatchC,
	}
	i.labelIdentityNamespaceIndex = map[string]sets.String{
		"testing": sets.NewString(labelB),
		"nomatch": sets.NewString(labelC),
	}
	selectorItemCCopy := &selectorItem{
		selector:          types.NewGroupSelector("", pSelDB, nil, nil, nil),
		policyKeys:        sets.NewString("policyA", "policyB"),
		labelIdentityKeys: sets.NewString(labelB, labelC),
	}
	selectorItemDCopy := &selectorItem{
		selector:          types.NewGroupSelector("testing", pSelDB, nil, nil, nil),
		policyKeys:        sets.NewString("policyA"),
		labelIdentityKeys: sets.NewString(labelB),
	}
	i.selectorItems.Update(selectorItemCCopy)
	i.selectorItems.Update(selectorItemDCopy)
	for _, tt := range tests {
		i.DeletePolicySelectors(tt.policyKey)
		for k, l := range i.labelIdentities {
			if l.selectorItemKeys.Has(tt.staleSelector) {
				t.Errorf("Stale selector %s is not deleted from labelMatch %s", tt.staleSelector, k)
			}
		}
		if _, exists, _ := i.selectorItems.GetByKey(tt.staleSelector); exists {
			t.Errorf("Stale selector %s is not deleted from selectorItem cache", tt.staleSelector)
		}
	}
}

func TestSetPolicySelectors(t *testing.T) {
	tests := []struct {
		name             string
		selectors        []*types.GroupSelector
		policyKey        string
		expIDs           []uint32
		expSelectorItems map[string]selectorItem
	}{
		{
			name: "new selector for policyA",
			selectors: []*types.GroupSelector{
				types.NewGroupSelector("", pSelWeb, nil, nil, nil),
			},
			policyKey: "policyA",
			expIDs:    []uint32{1},
			expSelectorItems: map[string]selectorItem{
				selectorItemA.selector.NormalizedName: {
					labelIdentityKeys: sets.NewString(labelA),
					policyKeys:        sets.NewString("policyA"),
				},
			},
		},
		{
			name: "updated selectors for policyA",
			selectors: []*types.GroupSelector{
				types.NewGroupSelector("", pSelWeb, nsSelTest, nil, nil),
				types.NewGroupSelector("", pSelDB, nil, nil, nil),
			},
			policyKey: "policyA",
			expIDs:    []uint32{1, 2, 3},
			expSelectorItems: map[string]selectorItem{
				selectorItemB.selector.NormalizedName: {
					labelIdentityKeys: sets.NewString(labelA),
					policyKeys:        sets.NewString("policyA"),
				},
				selectorItemC.selector.NormalizedName: {
					labelIdentityKeys: sets.NewString(labelB, labelC),
					policyKeys:        sets.NewString("policyA"),
				},
			},
		},
		{
			name: "existing selector and new selector for policyB",
			selectors: []*types.GroupSelector{
				types.NewGroupSelector("", pSelWeb, nil, nil, nil),
				types.NewGroupSelector("", pSelWeb, nsSelTest, nil, nil),
			},
			policyKey: "policyB",
			expIDs:    []uint32{1},
			expSelectorItems: map[string]selectorItem{
				selectorItemA.selector.NormalizedName: {
					labelIdentityKeys: sets.NewString(labelA),
					policyKeys:        sets.NewString("policyB"),
				},
				selectorItemB.selector.NormalizedName: {
					labelIdentityKeys: sets.NewString(labelA),
					policyKeys:        sets.NewString("policyA", "policyB"),
				},
				selectorItemC.selector.NormalizedName: {
					labelIdentityKeys: sets.NewString(labelB, labelC),
					policyKeys:        sets.NewString("policyA"),
				},
			},
		},
	}
	i := NewLabelIdentityIndex()
	i.labelIdentities = map[string]*labelIdentityMatch{
		labelA: labelMatchA,
		labelB: labelMatchB,
		labelC: labelMatchC,
	}
	i.labelIdentityNamespaceIndex = map[string]sets.String{
		"testing": sets.NewString(labelA, labelB),
		"nomatch": sets.NewString(labelC),
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matchedIDs := i.SetPolicySelectors(tt.selectors, tt.policyKey)
			assert.ElementsMatch(t, tt.expIDs, matchedIDs)
			assert.Equalf(t, len(tt.expSelectorItems), len(i.selectorItems.List()),
				"Unexpected numner of cached selectorItems in step %s", tt.name)
			for selKey, expSelItem := range tt.expSelectorItems {
				s, exists, _ := i.selectorItems.GetByKey(selKey)
				if !exists {
					t.Errorf("Selector %s is not added in step %s", selKey, tt.name)
				}
				sItem := s.(*selectorItem)
				assert.Truef(t, sItem.policyKeys.Equal(expSelItem.policyKeys),
					"Unexpected policy keys for selectorItem %s in step %s", selKey, tt.name)
				assert.Truef(t, sItem.labelIdentityKeys.Equal(expSelItem.labelIdentityKeys),
					"Unexpected labelIdentity keys for selectorItem %s in step %s", selKey, tt.name)
			}
		})
	}
}

func TestAddLabelIdentity(t *testing.T) {
	labelIdentityAOriginalID := uint32(1)
	tests := []struct {
		name              string
		normalizedLabel   string
		id                uint32
		originalID        *uint32
		expPolicyCalled   []string
		expLabelIdenities map[string]*labelIdentityMatch
	}{
		{
			name:            "Add label identity A",
			normalizedLabel: labelA,
			id:              1,
			expPolicyCalled: []string{"policyA", "policyB"},
			expLabelIdenities: map[string]*labelIdentityMatch{
				labelA: {
					id:               1,
					selectorItemKeys: sets.NewString(selectorItemA.getKey(), selectorItemB.getKey()),
				},
			},
		},
		{
			name:            "Update label identity A",
			normalizedLabel: labelA,
			id:              4,
			originalID:      &labelIdentityAOriginalID,
			expPolicyCalled: []string{"policyA", "policyB", "policyA", "policyB"},
			expLabelIdenities: map[string]*labelIdentityMatch{
				labelA: {
					id:               4,
					selectorItemKeys: sets.NewString(selectorItemA.getKey(), selectorItemB.getKey()),
				},
			},
		},
		{
			name:            "Add label identity B",
			normalizedLabel: labelB,
			id:              2,
			expPolicyCalled: []string{"policyA", "policyB", "policyD"},
			expLabelIdenities: map[string]*labelIdentityMatch{
				labelB: {
					id:               2,
					selectorItemKeys: sets.NewString(selectorItemC.getKey(), selectorItemD.getKey()),
				},
			},
		},
		{
			name:            "Add label identity C",
			normalizedLabel: labelC,
			id:              3,
			expPolicyCalled: []string{"policyA", "policyB"},
			expLabelIdenities: map[string]*labelIdentityMatch{
				labelC: {
					id:               3,
					selectorItemKeys: sets.NewString(selectorItemC.getKey()),
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			i := NewLabelIdentityIndex()
			allSelectorItems := []*selectorItem{
				selectorItemACopy, selectorItemBCopy, selectorItemCCopy, selectorItemDCopy, selectorItemECopy,
			}
			for _, s := range allSelectorItems {
				i.selectorItems.Update(s)
			}
			stopCh := make(chan struct{})
			defer close(stopCh)

			go i.Run(stopCh)
			var lock sync.Mutex
			var actualPoliciesCalled []string
			i.AddEventHandler(func(policyKey string) {
				lock.Lock()
				defer lock.Unlock()
				actualPoliciesCalled = append(actualPoliciesCalled, policyKey)
			})

			if tt.originalID != nil {
				i.AddLabelIdentity(tt.normalizedLabel, *tt.originalID)
			}
			i.AddLabelIdentity(tt.normalizedLabel, tt.id)
			// Wait for event handler to handle label add event
			time.Sleep(100 * time.Millisecond)
			lock.Lock()
			defer lock.Unlock()
			assert.ElementsMatchf(t, actualPoliciesCalled, tt.expPolicyCalled,
				"Unexpected policy sync calls, exp: %v, act: %v", tt.expPolicyCalled, actualPoliciesCalled)
			for key, l := range tt.expLabelIdenities {
				actLabelMatch := i.labelIdentities[key]
				if actLabelMatch.id != l.id {
					t.Errorf("Unexpected id cached for label %s in step %s", tt.normalizedLabel, tt.name)
				}
				if !actLabelMatch.selectorItemKeys.Equal(l.selectorItemKeys) {
					t.Errorf("Unexpected matched selectorItems for label %s in step %s", tt.normalizedLabel, tt.name)
				}
			}
		})
	}
}

func TestDeleteLabelIdentity(t *testing.T) {
	tests := []struct {
		name              string
		labelToDelete     string
		expPolicyCalled   []string
		expLabelIdenities map[string]*labelIdentityMatch
	}{
		{
			name:            "Delete label identity A",
			labelToDelete:   labelA,
			expPolicyCalled: []string{"policyA", "policyB"},
			expLabelIdenities: map[string]*labelIdentityMatch{
				labelB: {
					id:               2,
					selectorItemKeys: sets.NewString(selectorItemC.getKey(), selectorItemD.getKey()),
				},
				labelC: {
					id:               3,
					selectorItemKeys: sets.NewString(selectorItemC.getKey()),
				},
			},
		},
		{
			name:            "Delete label identity B",
			labelToDelete:   labelB,
			expPolicyCalled: []string{"policyA", "policyB", "policyD"},
			expLabelIdenities: map[string]*labelIdentityMatch{
				labelA: {
					id:               1,
					selectorItemKeys: sets.NewString(selectorItemA.getKey(), selectorItemB.getKey()),
				},
				labelC: {
					id:               3,
					selectorItemKeys: sets.NewString(selectorItemC.getKey()),
				},
			},
		},
		{
			name:            "Delete label identity C",
			labelToDelete:   labelC,
			expPolicyCalled: []string{"policyA", "policyB"},
			expLabelIdenities: map[string]*labelIdentityMatch{
				labelA: {
					id:               1,
					selectorItemKeys: sets.NewString(selectorItemA.getKey(), selectorItemB.getKey()),
				},
				labelB: {
					id:               2,
					selectorItemKeys: sets.NewString(selectorItemC.getKey(), selectorItemD.getKey()),
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			i := NewLabelIdentityIndex()
			allSelectorItems := []*selectorItem{
				selectorItemACopy, selectorItemBCopy, selectorItemCCopy, selectorItemDCopy, selectorItemECopy,
			}
			for _, s := range allSelectorItems {
				i.selectorItems.Update(s)
			}
			stopCh := make(chan struct{})
			defer close(stopCh)

			go i.Run(stopCh)
			var lock sync.Mutex
			// Preload the index with label identities to be deleted
			i.labelIdentities = map[string]*labelIdentityMatch{
				labelA: {
					id:               1,
					selectorItemKeys: sets.NewString(selectorItemA.getKey(), selectorItemB.getKey()),
				},
				labelB: {
					id:               2,
					selectorItemKeys: sets.NewString(selectorItemC.getKey(), selectorItemD.getKey()),
				},
				labelC: {
					id:               3,
					selectorItemKeys: sets.NewString(selectorItemC.getKey()),
				},
			}
			i.labelIdentityNamespaceIndex = map[string]sets.String{
				"testing": sets.NewString(labelA, labelB),
				"nomatch": sets.NewString(labelC),
			}
			var actualPoliciesCalled []string
			i.AddEventHandler(func(policyKey string) {
				lock.Lock()
				defer lock.Unlock()
				actualPoliciesCalled = append(actualPoliciesCalled, policyKey)
			})
			i.DeleteLabelIdentity(tt.labelToDelete)
			time.Sleep(100 * time.Millisecond)
			lock.Lock()
			defer lock.Unlock()
			assert.ElementsMatchf(t, actualPoliciesCalled, tt.expPolicyCalled,
				"Unexpected policy sync calls, exp: %v, act: %v", tt.expPolicyCalled, actualPoliciesCalled)
			for key, l := range tt.expLabelIdenities {
				actLabelMatch := i.labelIdentities[key]
				if actLabelMatch.id != l.id {
					t.Errorf("Unexpected id cached for label %s in step %s", tt.labelToDelete, tt.name)
				}
				if !actLabelMatch.selectorItemKeys.Equal(l.selectorItemKeys) {
					t.Errorf("Unexpected matched selectorItems for label %s in step %s", tt.labelToDelete, tt.name)
				}
			}
		})
	}
}
