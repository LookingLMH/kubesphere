/*

 Copyright 2019 The KubeSphere Authors.

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
package resources

import (
	"kubesphere.io/kubesphere/pkg/informers"
	"kubesphere.io/kubesphere/pkg/params"
	"sort"
	"strings"

	"k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/labels"
)

type daemonSetSearcher struct {
}

func daemonSetStatus(item *v1.DaemonSet) string {
	if item.Status.NumberAvailable == 0 {
		return stopped
	} else if item.Status.DesiredNumberScheduled == item.Status.NumberAvailable {
		return running
	} else {
		return updating
	}
}

// Exactly Match
func (*daemonSetSearcher) match(match map[string]string, item *v1.DaemonSet) bool {
	for k, v := range match {
		switch k {
		case status:
			if daemonSetStatus(item) != v {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func (*daemonSetSearcher) fuzzy(fuzzy map[string]string, item *v1.DaemonSet) bool {

	for k, v := range fuzzy {
		switch k {
		case name:
			if !strings.Contains(item.Name, v) && !strings.Contains(item.Labels[displayName], v) {
				return false
			}
		case label:
			if !searchFuzzy(item.Labels, "", v) {
				return false
			}
		case annotation:
			if !searchFuzzy(item.Annotations, "", v) {
				return false
			}
			return false
		case app:
			if !strings.Contains(item.Labels[chart], v) && !strings.Contains(item.Labels[release], v) {
				return false
			}
		case keyword:
			if !strings.Contains(item.Name, v) && !searchFuzzy(item.Labels, "", v) && !searchFuzzy(item.Annotations, "", v) {
				return false
			}
		default:
			if !searchFuzzy(item.Labels, k, v) && !searchFuzzy(item.Annotations, k, v) {
				return false
			}
		}
	}

	return true
}

func (*daemonSetSearcher) compare(a, b *v1.DaemonSet, orderBy string) bool {
	switch orderBy {
	case createTime:
		return a.CreationTimestamp.Time.Before(b.CreationTimestamp.Time)
	case name:
		fallthrough
	default:
		return strings.Compare(a.Name, b.Name) <= 0
	}
}

func (s *daemonSetSearcher) search(namespace string, conditions *params.Conditions, orderBy string, reverse bool) ([]interface{}, error) {
	daemonSets, err := informers.SharedInformerFactory().Apps().V1().DaemonSets().Lister().DaemonSets(namespace).List(labels.Everything())

	if err != nil {
		return nil, err
	}

	result := make([]*v1.DaemonSet, 0)

	if len(conditions.Match) == 0 && len(conditions.Fuzzy) == 0 {
		result = daemonSets
	} else {
		for _, item := range daemonSets {
			if s.match(conditions.Match, item) && s.fuzzy(conditions.Fuzzy, item) {
				result = append(result, item)
			}
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if reverse {
			tmp := i
			i = j
			j = tmp
		}
		return s.compare(result[i], result[j], orderBy)
	})

	r := make([]interface{}, 0)
	for _, i := range result {
		r = append(r, i)
	}
	return r, nil
}
