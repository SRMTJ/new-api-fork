package controller

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Calcium-Ion/go-epay/epay"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

type LZSubscriptionEpayPayRequest struct {
	UserID        int    `json:"userId"`
	PlanID        int    `json:"planId"`
	PaymentMethod string `json:"paymentMethod"`
}

func LZSubscriptionRequestEpay(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}

	var req LZSubscriptionEpayPayRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.UserID <= 0 || req.PlanID <= 0 {
		common.ApiErrorMsg(c, "参数错误")
		return
	}

	user, err := model.GetUserById(req.UserID, true)
	if err != nil || user == nil {
		common.ApiErrorMsg(c, "用户不存在")
		return
	}

	plan, err := model.GetSubscriptionPlanById(req.PlanID)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if !plan.Enabled {
		common.ApiErrorMsg(c, "套餐未启用")
		return
	}
	if plan.PriceAmount < 0.01 {
		common.ApiErrorMsg(c, "套餐金额过低")
		return
	}
	if !operation_setting.ContainsPayMethod(req.PaymentMethod) {
		common.ApiErrorMsg(c, "支付方式不存在")
		return
	}

	if plan.MaxPurchasePerUser > 0 {
		count, err := model.CountUserSubscriptionsByPlan(req.UserID, plan.Id)
		if err != nil {
			common.ApiError(c, err)
			return
		}
		if count >= int64(plan.MaxPurchasePerUser) {
			common.ApiErrorMsg(c, "已达到该套餐购买上限")
			return
		}
	}

	callBackAddress := service.GetCallbackAddress()
	returnURL, err := url.Parse(callBackAddress + "/api/subscription/epay/return")
	if err != nil {
		common.ApiErrorMsg(c, "回调地址配置错误")
		return
	}
	notifyURL, err := url.Parse(callBackAddress + "/api/subscription/epay/notify")
	if err != nil {
		common.ApiErrorMsg(c, "回调地址配置错误")
		return
	}

	tradeNo := fmt.Sprintf("%s%d", common.GetRandomString(6), time.Now().Unix())
	tradeNo = fmt.Sprintf("SUBUSR%dNO%s", req.UserID, tradeNo)

	client := GetEpayClient()
	if client == nil {
		common.ApiErrorMsg(c, "当前管理员未配置支付信息")
		return
	}

	order := &model.SubscriptionOrder{
		UserId:          req.UserID,
		PlanId:          plan.Id,
		Money:           plan.PriceAmount,
		TradeNo:         tradeNo,
		PaymentMethod:   req.PaymentMethod,
		PaymentProvider: model.PaymentProviderEpay,
		CreateTime:      time.Now().Unix(),
		Status:          common.TopUpStatusPending,
	}
	if err := order.Insert(); err != nil {
		common.ApiErrorMsg(c, "创建订单失败")
		return
	}

	uri, params, err := client.Purchase(&epay.PurchaseArgs{
		Type:           req.PaymentMethod,
		ServiceTradeNo: tradeNo,
		Name:           fmt.Sprintf("SUB:%s", plan.Title),
		Money:          strconv.FormatFloat(plan.PriceAmount, 'f', 2, 64),
		Device:         epay.PC,
		NotifyUrl:      notifyURL,
		ReturnUrl:      returnURL,
	})
	if err != nil {
		_ = model.ExpireSubscriptionOrder(tradeNo, model.PaymentProviderEpay)
		common.ApiErrorMsg(c, "拉起支付失败")
		return
	}

	common.ApiSuccess(c, gin.H{
		"tradeNo": tradeNo,
		"status":  common.TopUpStatusPending,
		"money":   plan.PriceAmount,
		"plan": gin.H{
			"id":    plan.Id,
			"title": plan.Title,
		},
		"payment": gin.H{
			"url":    uri,
			"params": params,
		},
	})
}

func LZSubscriptionGetOrder(c *gin.Context) {
	tradeNo := strings.TrimSpace(c.Param("tradeNo"))
	if tradeNo == "" {
		common.ApiErrorMsg(c, "订单号不能为空")
		return
	}

	order := model.GetSubscriptionOrderByTradeNo(tradeNo)
	if order == nil {
		common.ApiErrorMsg(c, "订单不存在")
		return
	}

	planTitle := ""
	if plan, err := model.GetSubscriptionPlanById(order.PlanId); err == nil && plan != nil {
		planTitle = plan.Title
	}

	var currentSubscription any = nil
	activeSubscriptions, err := model.GetAllActiveUserSubscriptions(order.UserId)
	if err == nil && len(activeSubscriptions) > 0 && activeSubscriptions[0].Subscription != nil {
		sub := activeSubscriptions[0].Subscription
		remaining := sub.AmountTotal - sub.AmountUsed
		if remaining < 0 {
			remaining = 0
		}
		currentSubscription = gin.H{
			"id":              sub.Id,
			"status":          sub.Status,
			"planId":          sub.PlanId,
			"amountTotal":     sub.AmountTotal,
			"amountUsed":      sub.AmountUsed,
			"amountRemaining": remaining,
			"startTime":       sub.StartTime,
			"endTime":         sub.EndTime,
			"updatedAt":       sub.UpdatedAt,
		}
	}

	common.ApiSuccess(c, gin.H{
		"order": gin.H{
			"tradeNo":         order.TradeNo,
			"status":          order.Status,
			"userId":          order.UserId,
			"planId":          order.PlanId,
			"planTitle":       planTitle,
			"money":           order.Money,
			"paymentMethod":   order.PaymentMethod,
			"paymentProvider": order.PaymentProvider,
			"createTime":      order.CreateTime,
			"completeTime":    order.CompleteTime,
			"providerPayload": order.ProviderPayload,
		},
		"currentSubscription": currentSubscription,
	})
}
