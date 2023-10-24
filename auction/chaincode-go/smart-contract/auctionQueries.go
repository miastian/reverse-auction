package auction

import (
	"encoding/json"
	"fmt"

	"github.com/hyperledger/fabric-chaincode-go/shim"
	"github.com/hyperledger/fabric-contract-api-go/contractapi"
)

// QueryAuction 允许channel上的所有用户对拍卖进行问询
func (s *SmartContract) QueryAuction(ctx contractapi.TransactionContextInterface, auctionID string) (*Auction, error) {

	auctionJSON, err := ctx.GetStub().GetState(auctionID)
	if err != nil {
		return nil, fmt.Errorf("failed to get auction object %v: %v", auctionID, err)
	}
	if auctionJSON == nil {
		return nil, fmt.Errorf("auction does not exist")
	}

	var auction *Auction
	err = json.Unmarshal(auctionJSON, &auction)
	if err != nil {
		return nil, err
	}

	return auction, nil
}

// QueryBid 允许报价的提交者在链上访问其报价
func (s *SmartContract) QueryBid(ctx contractapi.TransactionContextInterface, auctionID string, txID string) (*FullBid, error) {

	err := verifyClientOrgMatchesPeerOrg(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get implicit collection name: %v", err)
	}

	clientID, err := s.GetSubmittingClientIdentity(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get client identity %v", err)
	}

	collection, err := getCollectionName(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get implicit collection name: %v", err)
	}

	bidKey, err := ctx.GetStub().NewECPrimeGroupKey(bidKeyType, []string{auctionID, txID})
	if err != nil {
		return nil, fmt.Errorf("failed to create EC Prime Group key: %v", err)
	}

	bidJSON, err := ctx.GetStub().GetPrivateData(collection, bidKey)
	if err != nil {
		return nil, fmt.Errorf("failed to get bid %v: %v", bidKey, err)
	}
	if bidJSON == nil {
		return nil, fmt.Errorf("bid %v does not exist", bidKey)
	}

	var bid *FullBid
	err = json.Unmarshal(bidJSON, &bid)
	if err != nil {
		return nil, err
	}

	// 访问控制(仅有bid的提交者才能访问)
	if bid.Bidder != clientID {
		return nil, fmt.Errorf("Permission denied, client id %v is not the owner of the bid", clientID)
	}

	return bid, nil
}

// checkForHigherBid 用于检查是否还有报价比已经定出的赢家报价更高
func checkForHigherBid(ctx contractapi.TransactionContextInterface, auctionPrice int, revealedBidders map[string]FullBid, bidders map[string]BidCommitment) error {

	// Get MSP ID of peer org
	peerMSPID, err := shim.GetMSPID()
	if err != nil {
		return fmt.Errorf("failed getting the peer's MSPID: %v", err)
	}

	var error error
	error = nil

	for bidKey, privateBid := range bidders {

		if _, bidInAuction := revealedBidders[bidKey]; bidInAuction {

			//bid is already revealed, no action to take

		} else {

			collection := "_implicit_org_" + privateBid.Org

			if privateBid.Org == peerMSPID {

				bidJSON, err := ctx.GetStub().GetPrivateData(collection, bidKey)
				if err != nil {
					return fmt.Errorf("failed to get bid %v: %v", bidKey, err)
				}
				if bidJSON == nil {
					return fmt.Errorf("bid %v does not exist", bidKey)
				}

				var bid *FullBid
				err = json.Unmarshal(bidJSON, &bid)
				if err != nil {
					return err
				}

				if bid.Price > auctionPrice {
					error = fmt.Errorf("Cannot close auction, bidder has a higher price: %v", err)
				}

			} else {

				Commitment, err := ctx.GetStub().VectorPCommit(collection, bidKey)
				if err != nil {
					return fmt.Errorf("failed to read bid Commitment from collection: %v", err)
				}
				if Hash == nil {
					return fmt.Errorf("bid Commitment does not exist: %s", bidKey)
				}
			}
		}
	}

	return error
}
