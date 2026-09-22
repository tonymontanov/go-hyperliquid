/*
FILE: internal/ratelimit/weights.go

DESCRIPTION:
Request weights of the Hyperliquid API, as published on the official
"Rate limits and user limits" page. The exchange returns NO rate-limit headers,
so the SDK has to account for the weight of every request on its own side.

IP-BASED LIMIT: 1200 weight per minute, shared by all REST requests of one IP.
  - exchange actions : 1 + floor(batch_length / 40)
  - info, weight 2   : l2Book, allMids, clearinghouseState, orderStatus,
                       spotClearinghouseState, exchangeStatus
  - info, weight 60  : userRole
  - info, weight 20  : every other documented info request
  - additional weight per 20 items RETURNED: recentTrades, historicalOrders,
    userFills, userFillsByTime, fundingHistory, userFunding,
    nonUserFundingUpdates, twapHistory, userTwapSliceFills,
    userTwapSliceFillsByTime, delegatorHistory, delegatorRewards,
    validatorStats; candleSnapshot — per 60 items.

NOT DOCUMENTED (the SDK does not guess):
  - how much "additional weight" one block of 20 / 60 items costs. The SDK
    charges 1 per block, the minimum consistent with the wording; if the
    exchange starts publishing the number, change itemBlockWeight only.
  - how WebSocket post requests are weighted. The SDK charges them like their
    REST counterparts, which is the conservative reading.

ADDRESS-BASED LIMIT (actions only, per user; sub-accounts are separate users):
1 request per 1 USDC of cumulative traded volume plus an initial buffer of
10000; a batch of n orders / cancels costs n. See AddressBudget.
*/

package ratelimit

const (
	// IPWeightLimitPerMinute — documented IP budget.
	IPWeightLimitPerMinute int64 = 1200
	// ActionBatchDivisor — batch_length divisor of the action weight formula.
	ActionBatchDivisor int = 40

	// infoWeightLight — weight of the cheap info requests.
	infoWeightLight int64 = 2
	// infoWeightDefault — weight of every other info request.
	infoWeightDefault int64 = 20
	// infoWeightUserRole — weight of userRole.
	infoWeightUserRole int64 = 60
	// itemBlockWeight — assumed extra weight per block of returned items (see
	// file header: the number itself is not documented).
	itemBlockWeight int64 = 1
)

// ActionWeight returns the IP weight of an exchange action with the given
// number of batched items.
func ActionWeight(batchLen int) int64 {
	if batchLen < 0 {
		batchLen = 0
	}
	return 1 + int64(batchLen/ActionBatchDivisor)
}

// InfoWeight returns the base IP weight of an info request type.
func InfoWeight(infoType string) int64 {
	switch infoType {
	case "l2Book", "allMids", "clearinghouseState", "orderStatus", "spotClearinghouseState", "exchangeStatus":
		return infoWeightLight
	case "userRole":
		return infoWeightUserRole
	default:
		return infoWeightDefault
	}
}

// itemBlockSize returns the size of the block of returned items that costs
// extra weight for the info type, or 0 when the type has no such surcharge.
func itemBlockSize(infoType string) int {
	switch infoType {
	case "candleSnapshot":
		return 60
	case "recentTrades", "historicalOrders", "userFills", "userFillsByTime", "fundingHistory",
		"userFunding", "nonUserFundingUpdates", "twapHistory", "userTwapSliceFills",
		"userTwapSliceFillsByTime", "delegatorHistory", "delegatorRewards", "validatorStats":
		return 20
	default:
		return 0
	}
}

// InfoItemsWeight returns the additional weight charged AFTER the response is
// known, for info types whose cost depends on the number of returned items.
func InfoItemsWeight(infoType string, items int) int64 {
	var block int = itemBlockSize(infoType)
	if block == 0 || items <= 0 {
		return 0
	}
	return int64(items/block) * itemBlockWeight
}
