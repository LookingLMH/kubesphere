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
package authentication

import (
	"context"
	"errors"
	"fmt"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/endpoints/request"
	"net/http"
	"strings"

	"github.com/mholt/caddy/caddyhttp/httpserver"
	"k8s.io/api/rbac/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/kubernetes/pkg/util/slice"
	"kubesphere.io/kubesphere/pkg/informers"
	sliceutils "kubesphere.io/kubesphere/pkg/utils"
)

type Authentication struct {
	Rule Rule
	Next httpserver.Handler
}

type Rule struct {
	Path         string
	ExceptedPath []string
}

func (c Authentication) ServeHTTP(w http.ResponseWriter, r *http.Request) (int, error) {

	if httpserver.Path(r.URL.Path).Matches(c.Rule.Path) {

		for _, path := range c.Rule.ExceptedPath {
			if httpserver.Path(r.URL.Path).Matches(path) {
				return c.Next.ServeHTTP(w, r)
			}
		}

		attrs, err := getAuthorizerAttributes(r.Context())

		if err != nil {
			return http.StatusInternalServerError, err
		}

		permitted, err := permissionValidate(attrs)

		if err != nil {
			return http.StatusInternalServerError, err
		}

		if !permitted {
			err = k8serr.NewForbidden(schema.GroupResource{Group: attrs.GetAPIGroup(), Resource: attrs.GetResource()}, attrs.GetName(), fmt.Errorf("permission undefined"))
			return handleForbidden(w, err), nil
		}
	}

	return c.Next.ServeHTTP(w, r)

}

func handleForbidden(w http.ResponseWriter, err error) int {
	message := fmt.Sprintf("Forbidden,%s", err.Error())
	w.Header().Add("WWW-Authenticate", message)
	return http.StatusForbidden
}

func permissionValidate(attrs authorizer.Attributes) (bool, error) {

	permitted, err := clusterRoleValidate(attrs)

	if err != nil {
		return false, err
	}

	if permitted {
		return true, nil
	}

	if attrs.GetNamespace() != "" {
		permitted, err = roleValidate(attrs)

		if err != nil {
			return false, err
		}

		if permitted {
			return true, nil
		}
	}

	return false, nil
}

func roleValidate(attrs authorizer.Attributes) (bool, error) {
	roleBindingLister := informers.SharedInformerFactory().Rbac().V1().RoleBindings().Lister()
	roleLister := informers.SharedInformerFactory().Rbac().V1().Roles().Lister()
	roleBindings, err := roleBindingLister.RoleBindings(attrs.GetNamespace()).List(labels.Everything())

	if err != nil {
		return false, err
	}

	fullSource := attrs.GetResource()

	if attrs.GetSubresource() != "" {
		fullSource = fullSource + "/" + attrs.GetSubresource()
	}

	for _, roleBinding := range roleBindings {

		for _, subj := range roleBinding.Subjects {

			if (subj.Kind == v1.UserKind && subj.Name == attrs.GetUser().GetName()) ||
				(subj.Kind == v1.GroupKind && slice.ContainsString(attrs.GetUser().GetGroups(), subj.Name, nil)) {
				role, err := roleLister.Roles(attrs.GetNamespace()).Get(roleBinding.RoleRef.Name)

				if err != nil {
					return false, err
				}

				for _, rule := range role.Rules {
					if ruleMatchesRequest(rule, attrs.GetAPIGroup(), "", attrs.GetResource(), attrs.GetSubresource(), attrs.GetName(), attrs.GetVerb()) {
						return true, nil
					}
				}
			}
		}
	}

	return false, nil
}

func clusterRoleValidate(attrs authorizer.Attributes) (bool, error) {
	clusterRoleBindingLister := informers.SharedInformerFactory().Rbac().V1().ClusterRoleBindings().Lister()
	clusterRoleBindings, err := clusterRoleBindingLister.List(labels.Everything())
	clusterRoleLister := informers.SharedInformerFactory().Rbac().V1().ClusterRoles().Lister()
	if err != nil {
		return false, err
	}

	for _, clusterRoleBinding := range clusterRoleBindings {

		for _, subject := range clusterRoleBinding.Subjects {

			if (subject.Kind == v1.UserKind && subject.Name == attrs.GetUser().GetName()) ||
				(subject.Kind == v1.GroupKind && sliceutils.HasString(attrs.GetUser().GetGroups(), subject.Name)) {

				clusterRole, err := clusterRoleLister.Get(clusterRoleBinding.RoleRef.Name)

				if err != nil {
					return false, err
				}

				for _, rule := range clusterRole.Rules {
					if attrs.IsResourceRequest() {
						if ruleMatchesRequest(rule, attrs.GetAPIGroup(), "", attrs.GetResource(), attrs.GetSubresource(), attrs.GetName(), attrs.GetVerb()) {
							return true, nil
						}
					} else {
						if ruleMatchesRequest(rule, "", attrs.GetPath(), "", "", "", attrs.GetVerb()) {
							return true, nil
						}
					}

				}

			}
		}
	}

	return false, nil
}

func ruleMatchesResources(rule v1.PolicyRule, apiGroup string, resource string, subresource string, resourceName string) bool {

	if resource == "" {
		return false
	}

	if !sliceutils.HasString(rule.APIGroups, apiGroup) && !sliceutils.HasString(rule.APIGroups, v1.ResourceAll) {
		return false
	}

	if len(rule.ResourceNames) > 0 && !sliceutils.HasString(rule.ResourceNames, resourceName) {
		return false
	}

	combinedResource := resource

	if subresource != "" {
		combinedResource = combinedResource + "/" + subresource
	}

	for _, res := range rule.Resources {

		// match "*"
		if res == v1.ResourceAll || res == combinedResource {
			return true
		}

		// match "*/subresource"
		if len(subresource) > 0 && strings.HasPrefix(res, "*/") && subresource == strings.TrimLeft(res, "*/") {
			return true
		}
		// match "resource/*"
		if strings.HasSuffix(res, "/*") && resource == strings.TrimRight(res, "/*") {
			return true
		}
	}

	return false
}

func ruleMatchesRequest(rule v1.PolicyRule, apiGroup string, nonResourceURL string, resource string, subresource string, resourceName string, verb string) bool {

	if !sliceutils.HasString(rule.Verbs, verb) && !sliceutils.HasString(rule.Verbs, v1.VerbAll) {
		return false
	}

	if nonResourceURL == "" {
		return ruleMatchesResources(rule, apiGroup, resource, subresource, resourceName)
	} else {
		return ruleMatchesNonResource(rule, nonResourceURL)
	}
}

func ruleMatchesNonResource(rule v1.PolicyRule, nonResourceURL string) bool {

	if nonResourceURL == "" {
		return false
	}

	for _, spec := range rule.NonResourceURLs {
		if pathMatches(nonResourceURL, spec) {
			return true
		}
	}

	return false
}

func pathMatches(path, spec string) bool {
	if spec == "*" {
		return true
	}
	if spec == path {
		return true
	}
	if strings.HasSuffix(spec, "*") && strings.HasPrefix(path, strings.TrimRight(spec, "*")) {
		return true
	}
	return false
}

func getAuthorizerAttributes(ctx context.Context) (authorizer.Attributes, error) {
	attribs := authorizer.AttributesRecord{}

	user, ok := request.UserFrom(ctx)
	if ok {
		attribs.User = user
	}

	requestInfo, found := request.RequestInfoFrom(ctx)
	if !found {
		return nil, errors.New("no RequestInfo found in the context")
	}

	// Start with common attributes that apply to resource and non-resource requests
	attribs.ResourceRequest = requestInfo.IsResourceRequest
	attribs.Path = requestInfo.Path
	attribs.Verb = requestInfo.Verb

	attribs.APIGroup = requestInfo.APIGroup
	attribs.APIVersion = requestInfo.APIVersion
	attribs.Resource = requestInfo.Resource
	attribs.Subresource = requestInfo.Subresource
	attribs.Namespace = requestInfo.Namespace
	attribs.Name = requestInfo.Name

	return &attribs, nil
}
