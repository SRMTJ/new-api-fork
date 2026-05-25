package controller

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupLZEnterpriseControllerTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	gin.SetMode(gin.TestMode)
	common.UsingSQLite = true
	common.UsingMySQL = false
	common.UsingPostgreSQL = false
	common.RedisEnabled = false

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite db: %v", err)
	}
	model.DB = db
	model.LOG_DB = db

	if err := db.AutoMigrate(&model.User{}, &model.Token{}, &model.Log{}); err != nil {
		t.Fatalf("failed to migrate tables: %v", err)
	}

	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func TestProvisionLZEnterpriseCreatesAndReusesUserToken(t *testing.T) {
	db := setupLZEnterpriseControllerTestDB(t)

	body := `{"enterpriseCode":"ENT-001","enterpriseName":"测试企业","username":"lz_ent_001","password":"Passw0rd1","displayName":"测试企业","group":"default","tokenName":"ENT-001-token","tokenConfig":{"unlimitedQuota":true}}`
	first := callProvisionLZEnterprise(t, body)
	if !first.Success {
		t.Fatalf("provision failed: %s", first.Message)
	}
	firstData := first.Data
	if firstData.NewAPIUserID == 0 || firstData.TokenID == 0 || firstData.TokenKey == "" {
		t.Fatalf("missing provision data: %+v", firstData)
	}
	if !firstData.UserCreated || !firstData.TokenCreated {
		t.Fatalf("expected first call to create user and token: %+v", firstData)
	}

	second := callProvisionLZEnterprise(t, body)
	if !second.Success {
		t.Fatalf("second provision failed: %s", second.Message)
	}
	secondData := second.Data
	if secondData.NewAPIUserID != firstData.NewAPIUserID || secondData.TokenID != firstData.TokenID || secondData.TokenKey != firstData.TokenKey {
		t.Fatalf("expected idempotent provision, first=%+v second=%+v", firstData, secondData)
	}
	if secondData.UserCreated || secondData.TokenCreated {
		t.Fatalf("expected second call to reuse user and token: %+v", secondData)
	}

	var userCount int64
	var tokenCount int64
	if err := db.Model(&model.User{}).Count(&userCount).Error; err != nil {
		t.Fatalf("failed to count users: %v", err)
	}
	if err := db.Model(&model.Token{}).Count(&tokenCount).Error; err != nil {
		t.Fatalf("failed to count tokens: %v", err)
	}
	if userCount != 1 || tokenCount != 1 {
		t.Fatalf("expected one user and one token, got users=%d tokens=%d", userCount, tokenCount)
	}
}

func TestProvisionLZEnterpriseUpdatesUserByUserID(t *testing.T) {
	db := setupLZEnterpriseControllerTestDB(t)

	existing := model.User{
		Username:    "old_admin",
		Password:    "Passw0rd1",
		DisplayName: "旧管理员",
		Role:        common.RoleCommonUser,
		Status:      common.UserStatusEnabled,
		Group:       "default",
	}
	require.NoError(t, existing.Insert(0))

	firstBody := fmt.Sprintf(`{"enterpriseCode":"ENT-100","enterpriseName":"企业100","userId":%d,"username":"13800138000","password":"Passw0rd2","displayName":"企业100管理员","group":"default","tokenName":"ENT-100-token","tokenConfig":{"unlimitedQuota":true}}`, existing.Id)
	first := callProvisionLZEnterprise(t, firstBody)
	if !first.Success {
		t.Fatalf("provision failed: %s", first.Message)
	}
	firstData := first.Data
	require.Equal(t, existing.Id, firstData.NewAPIUserID)
	require.Equal(t, "13800138000", firstData.Username)
	require.True(t, firstData.TokenCreated)

	second := callProvisionLZEnterprise(t, firstBody)
	if !second.Success {
		t.Fatalf("second provision failed: %s", second.Message)
	}
	secondData := second.Data
	require.Equal(t, firstData.NewAPIUserID, secondData.NewAPIUserID)
	require.Equal(t, firstData.TokenID, secondData.TokenID)
	require.Equal(t, firstData.TokenKey, secondData.TokenKey)
	require.False(t, secondData.UserCreated)
	require.False(t, secondData.TokenCreated)

	refreshed, err := model.GetUserById(existing.Id, true)
	require.NoError(t, err)
	require.Equal(t, "13800138000", refreshed.Username)
	require.True(t, common.ValidatePasswordAndHash("Passw0rd2", refreshed.Password))

	var userCount int64
	require.NoError(t, db.Model(&model.User{}).Count(&userCount).Error)
	require.EqualValues(t, 1, userCount)
}

func TestProvisionLZEnterpriseRejectsUsernameConflictWhenUsingUserID(t *testing.T) {
	setupLZEnterpriseControllerTestDB(t)

	firstUser := model.User{
		Username:    "user_a",
		Password:    "Passw0rd1",
		DisplayName: "用户A",
		Role:        common.RoleCommonUser,
		Status:      common.UserStatusEnabled,
		Group:       "default",
	}
	secondUser := model.User{
		Username:    "user_b",
		Password:    "Passw0rd1",
		DisplayName: "用户B",
		Role:        common.RoleCommonUser,
		Status:      common.UserStatusEnabled,
		Group:       "default",
	}
	require.NoError(t, firstUser.Insert(0))
	require.NoError(t, secondUser.Insert(0))

	body := fmt.Sprintf(`{"enterpriseCode":"ENT-200","enterpriseName":"企业200","userId":%d,"username":"user_a","password":"Passw0rd2","displayName":"企业200管理员","group":"default","tokenName":"ENT-200-token","tokenConfig":{"unlimitedQuota":true}}`, secondUser.Id)
	resp := callProvisionLZEnterprise(t, body)
	require.False(t, resp.Success)
	require.Equal(t, "username already exists", resp.Message)
}

type lzEnterpriseProvisionTestResponse struct {
	Success bool                          `json:"success"`
	Message string                        `json:"message"`
	Data    lzEnterpriseProvisionTestData `json:"data"`
}

type lzEnterpriseProvisionTestData struct {
	NewAPIUserID int    `json:"newApiUserId"`
	Username     string `json:"username"`
	TokenID      int    `json:"tokenId"`
	TokenKey     string `json:"tokenKey"`
	UserCreated  bool   `json:"userCreated"`
	TokenCreated bool   `json:"tokenCreated"`
}

func callProvisionLZEnterprise(t *testing.T, body string) lzEnterpriseProvisionTestResponse {
	t.Helper()

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/lz/enterprise/provision", bytes.NewBufferString(body))
	ctx.Request.Header.Set("Content-Type", "application/json")

	ProvisionLZEnterprise(ctx)

	var response lzEnterpriseProvisionTestResponse
	if err := common.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to decode response: %v body=%s", err, recorder.Body.String())
	}
	return response
}
