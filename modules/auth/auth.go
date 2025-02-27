// Copyright 2014 The Gogs Authors. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package auth

import (
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/Unknwon/com"
	"github.com/Unknwon/macaron"
	"github.com/macaron-contrib/binding"
	"github.com/macaron-contrib/session"

	"github.com/gogits/gogs/models"
	"github.com/gogits/gogs/modules/base"
	"github.com/gogits/gogs/modules/log"
	"github.com/gogits/gogs/modules/setting"
	"github.com/gogits/gogs/modules/uuid"
)

func IsAPIPath(url string) bool {
	return strings.HasPrefix(url, "/api/")
}

// SignedInId returns the id of signed in user.
func SignedInId(req *http.Request, sess session.Store) int64 {
	if !models.HasEngine {
		return 0
	}

	// API calls need to check access token.
	if IsAPIPath(req.URL.Path) {
		auHead := req.Header.Get("Authorization")
		if len(auHead) > 0 {
			auths := strings.Fields(auHead)
			if len(auths) == 2 && auths[0] == "token" {
				t, err := models.GetAccessTokenBySHA(auths[1])
				if err != nil {
					if err != models.ErrAccessTokenNotExist {
						log.Error(4, "GetAccessTokenBySHA: %v", err)
					}
					return 0
				}
				t.Updated = time.Now()
				if err = models.UpdateAccessToekn(t); err != nil {
					log.Error(4, "UpdateAccessToekn: %v", err)
				}
				return t.UID
			}
		}
	}

	uid := sess.Get("uid")
	if uid == nil {
		return 0
	}
	if id, ok := uid.(int64); ok {
		if _, err := models.GetUserByID(id); err != nil {
			if !models.IsErrUserNotExist(err) {
				log.Error(4, "GetUserById: %v", err)
			}
			return 0
		}
		return id
	}
	return 0
}

// SignedInUser returns the user object of signed user.
// It returns a bool value to indicate whether user uses basic auth or not.
func SignedInUser(req *http.Request, sess session.Store) (*models.User, bool) {
	if !models.HasEngine {
		return nil, false
	}

	uid := SignedInId(req, sess)

	if uid <= 0 {
		if setting.Service.EnableReverseProxyAuth {
			webAuthUser := req.Header.Get(setting.ReverseProxyAuthUser)
			if len(webAuthUser) > 0 {
				u, err := models.GetUserByName(webAuthUser)
				if err != nil {
					if !models.IsErrUserNotExist(err) {
						log.Error(4, "GetUserByName: %v", err)
						return nil, false
					}

					// Check if enabled auto-registration.
					if setting.Service.EnableReverseProxyAutoRegister {
						u := &models.User{
							Name:     webAuthUser,
							Email:    uuid.NewV4().String() + "@localhost",
							Passwd:   webAuthUser,
							IsActive: true,
						}
						if err = models.CreateUser(u); err != nil {
							// FIXME: should I create a system notice?
							log.Error(4, "CreateUser: %v", err)
							return nil, false
						} else {
							return u, false
						}
					}
				}
				return u, false
			}
		}

		// Check with basic auth.
		baHead := req.Header.Get("Authorization")
		if len(baHead) > 0 {
			auths := strings.Fields(baHead)
			if len(auths) == 2 && auths[0] == "Basic" {
				uname, passwd, _ := base.BasicAuthDecode(auths[1])

				u, err := models.UserSignIn(uname, passwd)
				if err != nil {
					if !models.IsErrUserNotExist(err) {
						log.Error(4, "UserSignIn: %v", err)
					}
					return nil, false
				}

				return u, true
			}
		}
		return nil, false
	}

	u, err := models.GetUserByID(uid)
	if err != nil {
		log.Error(4, "GetUserById: %v", err)
		return nil, false
	}
	return u, false
}

type Form interface {
	binding.Validator
}

func init() {
	binding.SetNameMapper(com.ToSnakeCase)
}

// AssignForm assign form values back to the template data.
func AssignForm(form interface{}, data map[string]interface{}) {
	typ := reflect.TypeOf(form)
	val := reflect.ValueOf(form)

	if typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
		val = val.Elem()
	}

	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)

		fieldName := field.Tag.Get("form")
		// Allow ignored fields in the struct
		if fieldName == "-" {
			continue
		} else if len(fieldName) == 0 {
			fieldName = com.ToSnakeCase(field.Name)
		}

		data[fieldName] = val.Field(i).Interface()
	}
}

func getSize(field reflect.StructField, prefix string) string {
	for _, rule := range strings.Split(field.Tag.Get("binding"), ";") {
		if strings.HasPrefix(rule, prefix) {
			return rule[len(prefix) : len(rule)-1]
		}
	}
	return ""
}

func GetSize(field reflect.StructField) string {
	return getSize(field, "Size(")
}

func GetMinSize(field reflect.StructField) string {
	return getSize(field, "MinSize(")
}

func GetMaxSize(field reflect.StructField) string {
	return getSize(field, "MaxSize(")
}

func validate(errs binding.Errors, data map[string]interface{}, f Form, l macaron.Locale) binding.Errors {
	if errs.Len() == 0 {
		return errs
	}

	data["HasError"] = true
	AssignForm(f, data)

	typ := reflect.TypeOf(f)
	val := reflect.ValueOf(f)

	if typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
		val = val.Elem()
	}

	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)

		fieldName := field.Tag.Get("form")
		// Allow ignored fields in the struct
		if fieldName == "-" {
			continue
		}

		if errs[0].FieldNames[0] == field.Name {
			data["Err_"+field.Name] = true

			trName := field.Tag.Get("locale")
			if len(trName) == 0 {
				trName = l.Tr("form." + field.Name)
			} else {
				trName = l.Tr(trName)
			}

			switch errs[0].Classification {
			case binding.ERR_REQUIRED:
				data["ErrorMsg"] = trName + l.Tr("form.require_error")
			case binding.ERR_ALPHA_DASH:
				data["ErrorMsg"] = trName + l.Tr("form.alpha_dash_error")
			case binding.ERR_ALPHA_DASH_DOT:
				data["ErrorMsg"] = trName + l.Tr("form.alpha_dash_dot_error")
			case binding.ERR_SIZE:
				data["ErrorMsg"] = trName + l.Tr("form.size_error", GetSize(field))
			case binding.ERR_MIN_SIZE:
				data["ErrorMsg"] = trName + l.Tr("form.min_size_error", GetMinSize(field))
			case binding.ERR_MAX_SIZE:
				data["ErrorMsg"] = trName + l.Tr("form.max_size_error", GetMaxSize(field))
			case binding.ERR_EMAIL:
				data["ErrorMsg"] = trName + l.Tr("form.email_error")
			case binding.ERR_URL:
				data["ErrorMsg"] = trName + l.Tr("form.url_error")
			default:
				data["ErrorMsg"] = l.Tr("form.unknown_error") + " " + errs[0].Classification
			}
			return errs
		}
	}
	return errs
}
