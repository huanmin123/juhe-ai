package managementopenaioauth

import (
	"net/http"
	"time"
)

const ContractVersion = 1

type Operation string

const (
	OperationAuthURL                     Operation = "auth_url"
	OperationCreateFromCode              Operation = "create_from_code"
	OperationCreateFromRefreshToken      Operation = "create_from_refresh_token"
	OperationRefreshToken                Operation = "refresh_token"
	OperationReauthorizeFromCode         Operation = "reauthorize_from_code"
	OperationReauthorizeFromRefreshToken Operation = "reauthorize_from_refresh_token"
)

type OperationContract struct {
	Operation                 Operation
	Path                      string
	SuccessStatus             int
	MutationGuardOperationKey string
	ProcessingTTL             time.Duration
}

var operationContracts = [...]OperationContract{
	{Operation: OperationAuthURL, Path: "/auth-url", SuccessStatus: http.StatusOK},
	{Operation: OperationCreateFromCode, Path: "/create-from-code", SuccessStatus: http.StatusCreated, MutationGuardOperationKey: "openai_oauth.create_from_code", ProcessingTTL: 180 * time.Second},
	{Operation: OperationCreateFromRefreshToken, Path: "/create-from-refresh-token", SuccessStatus: http.StatusCreated, MutationGuardOperationKey: "openai_oauth.create_from_refresh_token", ProcessingTTL: 180 * time.Second},
	{Operation: OperationRefreshToken, Path: "/accounts/{id}/refresh-token", SuccessStatus: http.StatusOK},
	{Operation: OperationReauthorizeFromCode, Path: "/accounts/{id}/reauthorize-from-code", SuccessStatus: http.StatusOK},
	{Operation: OperationReauthorizeFromRefreshToken, Path: "/accounts/{id}/reauthorize-from-refresh-token", SuccessStatus: http.StatusOK},
}

func (o Operation) Valid() bool {
	for _, contract := range operationContracts {
		if contract.Operation == o {
			return true
		}
	}
	return false
}

func OperationContracts() []OperationContract {
	contracts := make([]OperationContract, len(operationContracts))
	copy(contracts, operationContracts[:])
	return contracts
}
