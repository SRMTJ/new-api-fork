package controller

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type LZEnterpriseProvisionRequest struct {
	EnterpriseCode string                         `json:"enterpriseCode"`
	EnterpriseName string                         `json:"enterpriseName"`
	UserID         *int                           `json:"userId,omitempty"`
	Username       string                         `json:"username"`
	Password       string                         `json:"password"`
	DisplayName    string                         `json:"displayName"`
	Group          string                         `json:"group"`
	TokenName      string                         `json:"tokenName"`
	TokenConfig    LZEnterpriseProvisionTokenSpec `json:"tokenConfig"`
}

type LZEnterpriseProvisionTokenSpec struct {
	ExpiredTime        *int64   `json:"expiredTime"`
	RemainQuota        *int     `json:"remainQuota"`
	UnlimitedQuota     *bool    `json:"unlimitedQuota"`
	ModelLimitsEnabled *bool    `json:"modelLimitsEnabled"`
	ModelLimits        []string `json:"modelLimits"`
	AllowIps           *string  `json:"allowIps"`
	Group              string   `json:"group"`
	CrossGroupRetry    *bool    `json:"crossGroupRetry"`
}

type lzEnterpriseProvisionNormalized struct {
	EnterpriseCode string
	EnterpriseName string
	UserID         *int
	Username       string
	Password       string
	DisplayName    string
	Group          string
	TokenName      string
	TokenConfig    LZEnterpriseProvisionTokenSpec
}

// ProvisionLZEnterprise creates or updates the New API user/token pair required by LZServer enterprises.
func ProvisionLZEnterprise(c *gin.Context) {
	var req LZEnterpriseProvisionRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiErrorMsg(c, "invalid request body")
		return
	}
	normalized, err := normalizeLZEnterpriseProvisionRequest(req)
	if err != nil {
		common.ApiErrorMsg(c, err.Error())
		return
	}

	user, userCreated, err := provisionLZEnterpriseUser(normalized)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	token, tokenCreated, err := provisionLZEnterpriseToken(user.Id, normalized)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"enterpriseCode": normalized.EnterpriseCode,
			"newApiUserId":   user.Id,
			"username":       user.Username,
			"displayName":    user.DisplayName,
			"group":          user.Group,
			"tokenId":        token.Id,
			"tokenName":      token.Name,
			"tokenKey":       token.GetFullKey(),
			"userCreated":    userCreated,
			"tokenCreated":   tokenCreated,
		},
	})
}

func normalizeLZEnterpriseProvisionRequest(req LZEnterpriseProvisionRequest) (lzEnterpriseProvisionNormalized, error) {
	value := lzEnterpriseProvisionNormalized{
		EnterpriseCode: strings.TrimSpace(req.EnterpriseCode),
		EnterpriseName: strings.TrimSpace(req.EnterpriseName),
		UserID:         req.UserID,
		Username:       strings.TrimSpace(req.Username),
		Password:       strings.TrimSpace(req.Password),
		DisplayName:    strings.TrimSpace(req.DisplayName),
		Group:          strings.TrimSpace(req.Group),
		TokenName:      strings.TrimSpace(req.TokenName),
		TokenConfig:    req.TokenConfig,
	}
	if value.EnterpriseCode == "" {
		return value, errors.New("enterpriseCode is required")
	}
	if value.UserID != nil && *value.UserID <= 0 {
		return value, errors.New("userId must be greater than 0")
	}
	if value.Username == "" {
		return value, errors.New("username is required")
	}
	if len(value.Username) > model.UserNameMaxLength {
		return value, fmt.Errorf("username must be <= %d characters", model.UserNameMaxLength)
	}
	if value.DisplayName == "" {
		value.DisplayName = value.EnterpriseName
	}
	if value.DisplayName == "" {
		value.DisplayName = value.Username
	}
	if len(value.DisplayName) > 20 {
		value.DisplayName = value.DisplayName[:20]
	}
	if value.Group == "" {
		value.Group = "default"
	}
	if value.TokenName == "" {
		value.TokenName = value.Username + "-lzserver"
	}
	if len(value.TokenName) > 50 {
		value.TokenName = value.TokenName[:50]
	}
	return value, nil
}

func provisionLZEnterpriseUser(req lzEnterpriseProvisionNormalized) (*model.User, bool, error) {
	if req.UserID != nil {
		return provisionLZEnterpriseUserByID(req)
	}

	var user model.User
	err := model.DB.Unscoped().Where("username = ?", req.Username).First(&user).Error
	if err == nil {
		updates := map[string]interface{}{
			"display_name": req.DisplayName,
			"group":        req.Group,
			"status":       common.UserStatusEnabled,
			"role":         common.RoleCommonUser,
			"deleted_at":   nil,
		}
		if req.Password != "" {
			passwordHash, err := common.Password2Hash(req.Password)
			if err != nil {
				return nil, false, err
			}
			updates["password"] = passwordHash
		}
		if err := model.DB.Unscoped().Model(&model.User{}).Where("id = ?", user.Id).Updates(updates).Error; err != nil {
			return nil, false, err
		}
		if refreshed, err := model.GetUserById(user.Id, true); err == nil {
			return refreshed, false, nil
		}
		return &user, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, err
	}
	password := req.Password
	if password == "" {
		password = common.GetRandomString(16)
	}
	user = model.User{
		Username:    req.Username,
		Password:    password,
		DisplayName: req.DisplayName,
		Role:        common.RoleCommonUser,
		Status:      common.UserStatusEnabled,
		Group:       req.Group,
	}
	if err := user.Insert(0); err != nil {
		return nil, false, err
	}
	if refreshed, err := model.GetUserById(user.Id, true); err == nil {
		return refreshed, true, nil
	}
	return &user, true, nil
}

func provisionLZEnterpriseUserByID(req lzEnterpriseProvisionNormalized) (*model.User, bool, error) {
	var user model.User
	if err := model.DB.Unscoped().Where("id = ?", *req.UserID).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, errors.New("userId not found")
		}
		return nil, false, err
	}

	var conflict model.User
	conflictErr := model.DB.Unscoped().Where("username = ? AND id <> ?", req.Username, user.Id).First(&conflict).Error
	if conflictErr == nil {
		return nil, false, errors.New("username already exists")
	}
	if conflictErr != nil && !errors.Is(conflictErr, gorm.ErrRecordNotFound) {
		return nil, false, conflictErr
	}

	updates := map[string]interface{}{
		"username":     req.Username,
		"display_name": req.DisplayName,
		"group":        req.Group,
		"status":       common.UserStatusEnabled,
		"role":         common.RoleCommonUser,
		"deleted_at":   nil,
	}
	if req.Password != "" {
		passwordHash, err := common.Password2Hash(req.Password)
		if err != nil {
			return nil, false, err
		}
		updates["password"] = passwordHash
	}
	if err := model.DB.Unscoped().Model(&model.User{}).Where("id = ?", user.Id).Updates(updates).Error; err != nil {
		return nil, false, err
	}

	refreshed, err := model.GetUserById(user.Id, true)
	if err != nil {
		return nil, false, err
	}
	return refreshed, false, nil
}

func provisionLZEnterpriseToken(userID int, req lzEnterpriseProvisionNormalized) (*model.Token, bool, error) {
	var token model.Token
	err := model.DB.Where("user_id = ? AND name = ?", userID, req.TokenName).First(&token).Error
	if err == nil {
		updateTokenFromLZSpec(&token, req)
		if err := token.Update(); err != nil {
			return nil, false, err
		}
		if refreshed, err := model.GetTokenByIds(token.Id, userID); err == nil {
			return refreshed, false, nil
		}
		return &token, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, err
	}
	key, err := common.GenerateKey()
	if err != nil {
		return nil, false, err
	}
	token = model.Token{
		UserId:       userID,
		Name:         req.TokenName,
		Key:          key,
		Status:       common.TokenStatusEnabled,
		CreatedTime:  common.GetTimestamp(),
		AccessedTime: common.GetTimestamp(),
		ExpiredTime:  -1,
	}
	updateTokenFromLZSpec(&token, req)
	if err := token.Insert(); err != nil {
		return nil, false, err
	}
	return &token, true, nil
}

func updateTokenFromLZSpec(token *model.Token, req lzEnterpriseProvisionNormalized) {
	spec := req.TokenConfig
	token.Status = common.TokenStatusEnabled
	if spec.ExpiredTime != nil {
		token.ExpiredTime = *spec.ExpiredTime
	} else if token.ExpiredTime == 0 {
		token.ExpiredTime = -1
	}
	if spec.RemainQuota != nil {
		token.RemainQuota = *spec.RemainQuota
	}
	if spec.UnlimitedQuota != nil {
		token.UnlimitedQuota = *spec.UnlimitedQuota
	} else if token.Id == 0 {
		token.UnlimitedQuota = true
	}
	if spec.ModelLimitsEnabled != nil {
		token.ModelLimitsEnabled = *spec.ModelLimitsEnabled
	}
	if spec.ModelLimits != nil {
		token.ModelLimits = strings.Join(normalizeLZStringList(spec.ModelLimits), ",")
		token.ModelLimitsEnabled = token.ModelLimits != ""
	}
	if spec.AllowIps != nil {
		allowIps := strings.TrimSpace(*spec.AllowIps)
		token.AllowIps = &allowIps
	}
	if group := strings.TrimSpace(spec.Group); group != "" {
		token.Group = group
	} else if req.Group != "" {
		token.Group = req.Group
	}
	if spec.CrossGroupRetry != nil {
		token.CrossGroupRetry = *spec.CrossGroupRetry
	}
}

func normalizeLZStringList(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
