package keeper

import (
	"fmt"

	"github.com/irisnet/irishub/app/v1/auth"
	"github.com/irisnet/irishub/app/v3/service/internal/types"
	sdk "github.com/irisnet/irishub/types"
)

// RegisterResponseCallback registers a module callback for response handling
func (k Keeper) RegisterResponseCallback(moduleName string, respCallback types.ResponseCallback) sdk.Error {
	if _, ok := k.respCallbacks[moduleName]; ok {
		return types.ErrModuleNameRegistered(k.codespace, moduleName)
	}

	k.respCallbacks[moduleName] = respCallback

	return nil
}

// CreateRequestContext creates a request context with the specified params
func (k Keeper) CreateRequestContext(
	ctx sdk.Context,
	serviceName string,
	providers []sdk.AccAddress,
	consumer sdk.AccAddress,
	input string,
	serviceFeeCap sdk.Coins,
	timeout int64,
	repeated bool,
	repeatedFrequency uint64,
	repeatedTotal int64,
	state types.RequestContextState,
	respThreshold uint16,
	respHandler string,
) ([]byte, sdk.Error) {
	svcDef, found := k.GetServiceDefinition(ctx, serviceName)
	if !found {
		return nil, types.ErrUnknownServiceDefinition(k.codespace, serviceName)
	}

	if err := types.ValidateRequestInput(svcDef.Schemas, input); err != nil {
		return nil, err
	}

	params := k.GetParamSet(ctx)
	if timeout > params.MaxRequestTimeout {
		return nil, types.ErrInvalidRequest(k.codespace, fmt.Sprintf("timeout must not be greater than %d: %d", params.MaxRequestTimeout, timeout))
	}

	if timeout == 0 {
		timeout = params.MaxRequestTimeout
	}

	if repeated {
		if repeatedFrequency == 0 {
			repeatedFrequency = uint64(timeout)
		}

		if repeatedFrequency < uint64(timeout) {
			return nil, types.ErrInvalidRequest(k.codespace, fmt.Sprintf("repeated frequency [%d] must not be less than timeout [%d]", repeatedFrequency, timeout))
		}
	} else {
		repeatedFrequency = 0
		repeatedTotal = 0
	}

	batchCounter := uint64(0)

	requestContext := types.NewRequestContext(
		serviceName, providers, consumer, input, serviceFeeCap,
		timeout, repeated, repeatedFrequency, repeatedTotal,
		batchCounter, state, respThreshold, respHandler,
	)

	requestContextID := types.GenerateRequestContextID(ctx.BlockHeight(), k.GetIntraTxCounter(ctx))
	k.SetRequestContext(ctx, requestContextID, requestContext)

	return requestContextID, nil
}

// UpdateRequestContext updates the specified request context
func (k Keeper) UpdateRequestContext(
	ctx sdk.Context,
	requestContextID []byte,
	providers []sdk.AccAddress,
	serviceFeeCap sdk.Coins,
	repeatedFreq uint64,
	repeatedTotal int64,
) sdk.Error {
	requestContext, found := k.GetRequestContext(ctx, requestContextID)
	if !found {
		return types.ErrInvalidRequestContextID(k.codespace, "invalid request context ID")
	}

	if !requestContext.Repeated {
		return types.ErrRequestContextNonRepeated(k.codespace)
	}

	if len(providers) > 0 && requestContext.ResponseThreshold > 0 && len(providers) < int(requestContext.ResponseThreshold) {
		return types.ErrInvalidProviders(k.codespace, "length of providers must not be less than the response threshold")
	}

	if repeatedFreq > 0 && repeatedFreq < uint64(requestContext.Timeout) {
		return types.ErrInvalidRepeatedFreq(k.codespace, "repeated frequency must not be less than the timeout")
	}

	if repeatedTotal >= 1 && repeatedTotal <= int64(requestContext.BatchCounter) {
		return types.ErrInvalidRepeatedTotal(k.codespace, "updated repeated total must be greater than the current batch counter")
	}

	if len(providers) > 0 {
		requestContext.Providers = providers
	}

	if repeatedFreq > 0 {
		requestContext.RepeatedFrequency = repeatedFreq
	}

	if repeatedTotal != 0 {
		requestContext.RepeatedTotal = repeatedTotal
	}

	k.SetRequestContext(ctx, requestContextID, requestContext)

	return nil
}

// PauseRequestContext suspends the specified request context
func (k Keeper) PauseRequestContext(
	ctx sdk.Context,
	requestContextID []byte,
) sdk.Error {
	requestContext, found := k.GetRequestContext(ctx, requestContextID)
	if !found {
		return types.ErrInvalidRequestContextID(k.codespace, "invalid request context ID")
	}

	if !requestContext.Repeated {
		return types.ErrRequestContextNonRepeated(k.codespace)
	}

	if requestContext.State != types.RequestContextState(0x00) {
		return types.ErrRequestContextNotStarted(k.codespace)
	}

	requestContext.State = types.RequestContextState(0x01)
	k.DeleteNewBatchRequest(ctx, requestContextID)

	k.SetRequestContext(ctx, requestContextID, requestContext)

	return nil
}

// StartRequestContext starts the specified request context
func (k Keeper) StartRequestContext(
	ctx sdk.Context,
	requestContextID []byte,
) sdk.Error {
	requestContext, found := k.GetRequestContext(ctx, requestContextID)
	if !found {
		return types.ErrInvalidRequestContextID(k.codespace, "invalid request context ID")
	}

	if !requestContext.Repeated {
		return types.ErrRequestContextNonRepeated(k.codespace)
	}

	if requestContext.State != types.RequestContextState(0x01) {
		return types.ErrRequestContextNotPaused(k.codespace)
	}

	requestContext.State = types.RequestContextState(0x00)
	k.AddNewBatchRequest(ctx, requestContextID)

	k.SetRequestContext(ctx, requestContextID, requestContext)

	return nil
}

// KillRequestContext terminates the specified request context
func (k Keeper) KillRequestContext(
	ctx sdk.Context,
	requestContextID []byte,
) sdk.Error {
	requestContext, found := k.GetRequestContext(ctx, requestContextID)
	if !found {
		return types.ErrInvalidRequestContextID(k.codespace, "invalid request context ID")
	}

	if !requestContext.Repeated {
		return types.ErrRequestContextNonRepeated(k.codespace)
	}

	requestContext.State = types.RequestContextState(0x01)
	requestContext.RepeatedTotal = int64(requestContext.BatchCounter)

	k.SetRequestContext(ctx, requestContextID, requestContext)

	return nil
}

// SetRequestContext sets the specified request context
func (k Keeper) SetRequestContext(ctx sdk.Context, requestContextID []byte, requestContext types.RequestContext) {
	store := ctx.KVStore(k.storeKey)

	bz := k.cdc.MustMarshalBinaryLengthPrefixed(requestContext)
	store.Set(GetRequestContextKey(requestContextID), bz)
}

// GetRequestContext retrieves the specified request context
func (k Keeper) GetRequestContext(ctx sdk.Context, requestContextID []byte) (requestContext types.RequestContext, found bool) {
	store := ctx.KVStore(k.storeKey)

	bz := store.Get(GetRequestContextKey(requestContextID))
	if bz == nil {
		return requestContext, false
	}

	k.cdc.MustUnmarshalBinaryLengthPrefixed(bz, &requestContext)
	return requestContext, true
}

// AddNewBatchRequest adds a new batch request of the specified request context
func (k Keeper) AddNewBatchRequest(ctx sdk.Context, requestContextID []byte) {
	// TODO
}

// DeleteNewBatchRequest deletes the new batch request of the specified request context
func (k Keeper) DeleteNewBatchRequest(ctx sdk.Context, requestContextID []byte) {
	// TODO
}

// AddRequest
func (k Keeper) AddRequest(
	ctx sdk.Context,
	defChainID,
	defName,
	bindChainID,
	reqChainID string,
	consumer,
	provider sdk.AccAddress,
	methodID int16,
	input []byte,
	serviceFee sdk.Coins,
	profiling bool,
) (req types.SvcRequest, err sdk.Error) {
	binding, found := k.GetServiceBinding(ctx, defName, provider)
	if !found {
		return req, types.ErrUnknownServiceBinding(k.codespace)
	}

	if !binding.Available {
		return req, types.ErrServiceBindingUnavailable(k.codespace)
	}

	if profiling {
		if _, found := k.gk.GetProfiler(ctx, consumer); !found {
			return req, types.ErrNotProfiler(k.codespace, consumer)
		}
	}

	// TODO: service fee logic

	req = types.NewSvcRequest(
		defChainID, defName, bindChainID, reqChainID, consumer,
		provider, methodID, input, serviceFee, profiling,
	)

	counter := k.GetIntraTxCounter(ctx)
	k.SetIntraTxCounter(ctx, counter+1)

	req.RequestIntraTxCounter = counter
	req.RequestHeight = ctx.BlockHeight()

	params := k.GetParamSet(ctx)
	req.ExpirationHeight = req.RequestHeight + params.MaxRequestTimeout

	_, err = k.bk.SendCoins(ctx, req.Consumer, auth.ServiceRequestCoinsAccAddr, req.ServiceFee)
	if err != nil {
		return req, err
	}

	k.SetRequest(ctx, req)
	k.AddActiveRequest(ctx, req)
	k.AddRequestExpiration(ctx, req)

	return req, nil
}

// SetRequest
func (k Keeper) SetRequest(ctx sdk.Context, req types.SvcRequest) {
	store := ctx.KVStore(k.storeKey)

	bz := k.cdc.MustMarshalBinaryLengthPrefixed(req)
	store.Set(GetRequestKey(req.DefChainID, req.DefName, req.BindChainID,
		req.Provider, req.RequestHeight, req.RequestIntraTxCounter), bz)
}

// AddActiveRequest
func (k Keeper) AddActiveRequest(ctx sdk.Context, req types.SvcRequest) {
	store := ctx.KVStore(k.storeKey)

	bz := k.cdc.MustMarshalBinaryLengthPrefixed(req)
	store.Set(GetActiveRequestKey(req.DefChainID, req.DefName, req.BindChainID,
		req.Provider, req.RequestHeight, req.RequestIntraTxCounter), bz)
}

// DeleteActiveRequest
func (k Keeper) DeleteActiveRequest(ctx sdk.Context, req types.SvcRequest) {
	store := ctx.KVStore(k.storeKey)
	store.Delete(GetActiveRequestKey(req.DefChainID, req.DefName, req.BindChainID, req.Provider,
		req.RequestHeight, req.RequestIntraTxCounter))
}

// AddRequestExpiration
func (k Keeper) AddRequestExpiration(ctx sdk.Context, req types.SvcRequest) {
	store := ctx.KVStore(k.storeKey)

	bz := k.cdc.MustMarshalBinaryLengthPrefixed(req)
	store.Set(GetRequestsByExpirationIndexKeyByReq(req), bz)
}

// DeleteRequestExpiration
func (k Keeper) DeleteRequestExpiration(ctx sdk.Context, req types.SvcRequest) {
	store := ctx.KVStore(k.storeKey)
	store.Delete(GetRequestsByExpirationIndexKeyByReq(req))
}

// GetActiveRequest
func (k Keeper) GetActiveRequest(ctx sdk.Context, expHeight, reqHeight int64, counter int16) (req types.SvcRequest, found bool) {
	store := ctx.KVStore(k.storeKey)

	bz := store.Get(GetRequestsByExpirationIndexKey(expHeight, reqHeight, counter))
	if bz == nil {
		return req, false
	}

	k.cdc.MustUnmarshalBinaryLengthPrefixed(bz, &req)
	return req, true
}

// ActiveBindRequestsIterator returns an iterator for all the request in the Active Queue of specified service binding
func (k Keeper) ActiveBindRequestsIterator(ctx sdk.Context, defChainID, defName, bindChainID string, provider sdk.AccAddress) sdk.Iterator {
	store := ctx.KVStore(k.storeKey)
	return sdk.KVStorePrefixIterator(store, GetSubActiveRequestKey(defChainID, defName, bindChainID, provider))
}

// ActiveRequestQueueIterator returns an iterator for all the request in the Active Queue that expire by block height
func (k Keeper) ActiveRequestQueueIterator(ctx sdk.Context, height int64) sdk.Iterator {
	store := ctx.KVStore(k.storeKey)
	return sdk.KVStorePrefixIterator(store, GetRequestsByExpirationPrefix(height))
}

// ActiveAllRequestQueueIterator returns an iterator for all the requests in the active queue
func (k Keeper) ActiveAllRequestQueueIterator(store sdk.KVStore) sdk.Iterator {
	return sdk.KVStorePrefixIterator(store, activeRequestKey)
}

// AddResponse
func (k Keeper) AddResponse(
	ctx sdk.Context,
	reqChainID string,
	requestID string,
	provider sdk.AccAddress,
	output,
	errorMsg []byte,
) (resp types.SvcResponse, err sdk.Error) {
	expHeight, reqHeight, counter, _ := types.ConvertRequestID(requestID)

	req, found := k.GetActiveRequest(ctx, expHeight, reqHeight, counter)
	if !found {
		req.ExpirationHeight = expHeight
		req.RequestHeight = reqHeight
		req.RequestIntraTxCounter = counter

		return resp, types.ErrRequestNotActive(k.codespace, req.RequestID())
	}

	if !(provider.Equals(req.Provider)) {
		return resp, types.ErrNotMatchingProvider(k.codespace, provider)
	}

	if err := k.AddIncomingFee(ctx, provider, req.ServiceFee); err != nil {
		return resp, err
	}

	resp = types.NewSvcResponse(reqChainID, expHeight, reqHeight, counter, provider, req.Consumer, output, errorMsg)
	k.SetResponse(ctx, resp)

	// delete request from active request list and expiration list
	k.DeleteActiveRequest(ctx, req)
	k.DeleteRequestExpiration(ctx, req)

	return resp, nil
}

// SetResponse
func (k Keeper) SetResponse(ctx sdk.Context, resp types.SvcResponse) {
	store := ctx.KVStore(k.storeKey)

	bz := k.cdc.MustMarshalBinaryLengthPrefixed(resp)
	store.Set(GetResponseKey(resp.ReqChainID, resp.ExpirationHeight, resp.RequestHeight, resp.RequestIntraTxCounter), bz)
}

// GetResponse
func (k Keeper) GetResponse(ctx sdk.Context, reqChainID string, eHeight, rHeight int64, counter int16) (resp types.SvcResponse, found bool) {
	store := ctx.KVStore(k.storeKey)

	bz := store.Get(GetResponseKey(reqChainID, eHeight, rHeight, counter))
	if bz == nil {
		return resp, false
	}

	k.cdc.MustUnmarshalBinaryLengthPrefixed(bz, &resp)
	return resp, true
}

// Slash
func (k Keeper) Slash(ctx sdk.Context, binding types.ServiceBinding, slashCoins sdk.Coins) sdk.Error {
	deposit, hasNeg := binding.Deposit.SafeSub(slashCoins)
	if hasNeg {
		errMsg := fmt.Sprintf("%s is less than %s", binding.Deposit, slashCoins)
		panic(errMsg)
	}

	binding.Deposit = deposit
	minDeposit := k.getMinDeposit(ctx, binding.Pricing)

	if !binding.Deposit.IsAllGTE(minDeposit) {
		binding.Available = false
		binding.DisabledTime = ctx.BlockHeader().Time
	}

	_, err := k.bk.BurnCoins(ctx, auth.ServiceDepositCoinsAccAddr, slashCoins)
	if err != nil {
		return err
	}

	ctx.Logger().Info("Slash service provider", "provider", binding.Provider.String(), "slash_amount", slashCoins.String())
	k.SetServiceBinding(ctx, binding)

	return nil
}

// AddReturnFee add return fee for a particular consumer, if it is not existed will create a new
func (k Keeper) AddReturnFee(ctx sdk.Context, address sdk.AccAddress, coins sdk.Coins) {
	fee, found := k.GetReturnFee(ctx, address)
	if !found {
		k.SetReturnFee(ctx, address, coins)
		return
	}

	k.SetReturnFee(ctx, address, fee.Coins.Add(coins))
}

func (k Keeper) SetReturnFee(ctx sdk.Context, address sdk.AccAddress, coins sdk.Coins) {
	store := ctx.KVStore(k.storeKey)

	fee := types.NewReturnedFee(address, coins)
	bz := k.cdc.MustMarshalBinaryLengthPrefixed(fee)

	store.Set(GetReturnedFeeKey(address), bz)
}

func (k Keeper) DeleteReturnFee(ctx sdk.Context, address sdk.AccAddress) {
	store := ctx.KVStore(k.storeKey)
	store.Delete(GetReturnedFeeKey(address))
}

func (k Keeper) GetReturnFee(ctx sdk.Context, address sdk.AccAddress) (fee types.ReturnedFee, found bool) {
	store := ctx.KVStore(k.storeKey)

	bz := store.Get(GetReturnedFeeKey(address))
	if bz == nil {
		return fee, false
	}

	k.cdc.MustUnmarshalBinaryLengthPrefixed(bz, &fee)
	return fee, true
}

// refund fees from a particular consumer, and delete it
func (k Keeper) RefundFee(ctx sdk.Context, address sdk.AccAddress) sdk.Error {
	fee, found := k.GetReturnFee(ctx, address)
	if !found {
		return types.ErrReturnFeeNotExists(k.codespace, address)
	}

	_, err := k.bk.SendCoins(ctx, auth.ServiceRequestCoinsAccAddr, address, fee.Coins)
	if err != nil {
		return err
	}

	ctx.Logger().Info("Refund fees", "address", address.String(), "amount", fee.Coins.String())
	k.DeleteReturnFee(ctx, address)

	return nil
}

// Add incoming fee for a particular provider, if it is not existed will create a new
func (k Keeper) AddIncomingFee(ctx sdk.Context, address sdk.AccAddress, coins sdk.Coins) sdk.Error {
	params := k.GetParamSet(ctx)
	feeTax := params.ServiceFeeTax

	taxCoins := sdk.Coins{}
	for _, coin := range coins {
		taxAmount := sdk.NewDecFromInt(coin.Amount).Mul(feeTax).TruncateInt()
		taxCoins = taxCoins.Add(sdk.NewCoins(sdk.NewCoin(coin.Denom, taxAmount)))
	}

	_, err := k.bk.SendCoins(ctx, auth.ServiceRequestCoinsAccAddr, auth.ServiceTaxCoinsAccAddr, taxCoins)
	if err != nil {
		return err
	}

	incomingFee, hasNeg := coins.SafeSub(taxCoins)
	if hasNeg {
		errMsg := fmt.Sprintf("%s is less than %s", coins, taxCoins)
		return sdk.ErrInsufficientFunds(errMsg)
	}

	fee, _ := k.GetIncomingFee(ctx, address)
	k.SetIncomingFee(ctx, address, fee.Coins.Add(incomingFee))

	return nil
}

func (k Keeper) SetIncomingFee(ctx sdk.Context, address sdk.AccAddress, coins sdk.Coins) {
	store := ctx.KVStore(k.storeKey)

	fee := types.NewIncomingFee(address, coins)
	bz := k.cdc.MustMarshalBinaryLengthPrefixed(fee)

	store.Set(GetIncomingFeeKey(address), bz)
}

func (k Keeper) DeleteIncomingFee(ctx sdk.Context, address sdk.AccAddress) {
	store := ctx.KVStore(k.storeKey)
	store.Delete(GetIncomingFeeKey(address))
}

func (k Keeper) GetIncomingFee(ctx sdk.Context, address sdk.AccAddress) (fee types.IncomingFee, found bool) {
	store := ctx.KVStore(k.storeKey)

	bz := store.Get(GetIncomingFeeKey(address))
	if bz == nil {
		return fee, false
	}

	k.cdc.MustUnmarshalBinaryLengthPrefixed(bz, &fee)
	return fee, true
}

// withdraw fees from a particular provider, and delete it
func (k Keeper) WithdrawFee(ctx sdk.Context, address sdk.AccAddress) sdk.Error {
	fee, found := k.GetIncomingFee(ctx, address)
	if !found {
		return types.ErrWithdrawFeeNotExists(k.codespace, address)
	}

	_, err := k.bk.SendCoins(ctx, auth.ServiceRequestCoinsAccAddr, address, fee.Coins)
	if err != nil {
		return err
	}

	ctx.Logger().Info("Withdraw fees", "address", address.String(), "amount", fee.Coins.String())
	k.DeleteIncomingFee(ctx, address)

	return nil
}

func (k Keeper) WithdrawTax(ctx sdk.Context, trustee sdk.AccAddress, destAddress sdk.AccAddress, amt sdk.Coins) sdk.Error {
	if _, found := k.gk.GetTrustee(ctx, trustee); !found {
		return types.ErrNotTrustee(k.codespace, trustee)
	}

	_, err := k.bk.SendCoins(ctx, auth.ServiceTaxCoinsAccAddr, destAddress, amt)
	if err != nil {
		return err
	}

	return nil
}

// AllReturnedFeesIterator returns an iterator for all the returned fees
func (k Keeper) AllReturnedFeesIterator(ctx sdk.Context) sdk.Iterator {
	store := ctx.KVStore(k.storeKey)
	return sdk.KVStorePrefixIterator(store, returnedFeeKey)
}

// AllIncomingFeesIterator returns an iterator for all the incoming fees
func (k Keeper) AllIncomingFeesIterator(ctx sdk.Context) sdk.Iterator {
	store := ctx.KVStore(k.storeKey)
	return sdk.KVStorePrefixIterator(store, incomingFeeKey)
}

// RefundReturnedFees refunds all the returned fees
func (k Keeper) RefundReturnedFees(ctx sdk.Context) sdk.Error {
	iterator := k.AllReturnedFeesIterator(ctx)
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var returnedFee types.ReturnedFee
		k.cdc.MustUnmarshalBinaryLengthPrefixed(iterator.Value(), &returnedFee)

		_, err := k.bk.SendCoins(ctx, auth.ServiceRequestCoinsAccAddr, returnedFee.Address, returnedFee.Coins)
		if err != nil {
			return err
		}
	}

	return nil
}

// RefundIncomingFees refunds all the incoming fees
func (k Keeper) RefundIncomingFees(ctx sdk.Context) sdk.Error {
	iterator := k.AllIncomingFeesIterator(ctx)
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var incomingFee types.IncomingFee
		k.cdc.MustUnmarshalBinaryLengthPrefixed(iterator.Value(), &incomingFee)

		_, err := k.bk.SendCoins(ctx, auth.ServiceRequestCoinsAccAddr, incomingFee.Address, incomingFee.Coins)
		if err != nil {
			return err
		}
	}

	return nil
}

// RefundServiceFees refunds the service fees of all the active requests
func (k Keeper) RefundServiceFees(ctx sdk.Context) sdk.Error {
	store := ctx.KVStore(k.storeKey)

	iterator := k.ActiveAllRequestQueueIterator(store)
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var request types.SvcRequest
		k.cdc.MustUnmarshalBinaryLengthPrefixed(iterator.Value(), &request)

		_, err := k.bk.SendCoins(ctx, auth.ServiceRequestCoinsAccAddr, request.Consumer, request.ServiceFee)
		if err != nil {
			return err
		}
	}

	return nil
}

// GetIntraTxCounter get the current in-block request operation counter
func (k Keeper) GetIntraTxCounter(ctx sdk.Context) int16 {
	store := ctx.KVStore(k.storeKey)

	b := store.Get(intraTxCounterKey)
	if b == nil {
		return 0
	}

	var counter int16
	k.cdc.MustUnmarshalBinaryLengthPrefixed(b, &counter)

	return counter
}

// SetIntraTxCounter set the current in-block request counter
func (k Keeper) SetIntraTxCounter(ctx sdk.Context, counter int16) {
	store := ctx.KVStore(k.storeKey)

	bz := k.cdc.MustMarshalBinaryLengthPrefixed(counter)
	store.Set(intraTxCounterKey, bz)
}
