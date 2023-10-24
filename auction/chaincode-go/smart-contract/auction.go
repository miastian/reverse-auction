package auction

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/hyperledger/fabric-contract-api-go/contractapi"
	"github.com/wrv/bp-go"
)

type SmartContract struct {
	contractapi.Contract
}

// Auction data
type Auction struct {
	Type         string             `json:"objectType"`
	ItemSold     string             `json:"item"`
	Seller       string             `json:"seller"`
	Orgs         []string           `json:"organizations"`
	PrivateBids  map[string]BidCommitment `json:"privateBids"`
	RevealedBid  map[string]FullBid `json:"revealedbid"`
	Winner       string             `json:"winner"`
	Price        int                `json:"price"`
	Status       string             `json:"status"`
}


// FullBid is the structure of a revealed bid
type FullBid struct {
	Type     string `json:"objectType"`
	Price    int    `json:"price"`
	Org      string `json:"org"`
	Bidder   string `json:"bidder"`
}

// BidCommitment is the structure of a private bid
type BidCommitment struct {
	Org  string `json:"org"`
	Commitment string `json:"commitment"`
}

const bidKeyType = "bid"

// CreateAuction在会在channel上创建一个拍卖
// 提交CreateAuction交易的用户就是该拍卖的seller
func (s *SmartContract) CreateAuction(ctx contractapi.TransactionContextInterface, auctionID string, itemsold string) error {

	// 获取提交交易用户的ID
	clientID, err := s.GetSubmittingClientIdentity(ctx)
	if err != nil {
		return fmt.Errorf("failed to get client identity %v", err)
	}

	// 获取提交交易用户的组织（orgID)
	clientOrgID, err := ctx.GetClientIdentity().GetMSPID()
	if err != nil {
		return fmt.Errorf("failed to get client identity %v", err)
	}

	bidders := make(map[string]BidHash)
	revealedBids := make(map[string]FullBid)

	auction := Auction{
		Type:         "auction",
		ItemSold:     itemsold,
		Price:        0,
		Seller:       clientID,
		Orgs:         []string{clientOrgID},
		PrivateBids:  bidders,
		RevealedBids: revealedBids,
		Winner:       "",
		Status:       "open",
	}

	auctionJSON, err := json.Marshal(auction)
	if err != nil {
		return err
	}

	// 将auction放到区块链上，更新公共账本
	err = ctx.GetStub().PutState(auctionID, auctionJSON)
	if err != nil {
		return fmt.Errorf("failed to put auction in public data: %v", err)
	}

	// 将seller作为该拍卖的背书者（endoreser）
	err = setAssetStateBasedEndorsement(ctx, auctionID, clientOrgID)
	if err != nil {
		return fmt.Errorf("failed setting state based endorsement for new organization: %v", err)
	}

	return nil
}

// Bid 用于添加报价
// 报价储存在报价者节点所在组织所在的私有数据集中
// 该函数返回值为交易的ID以便用户能够识别和查询其报价
func (s *SmartContract) Bid(ctx contractapi.TransactionContextInterface, auctionID string) (string, error) {

	// 获取transient map中的数据
	transientMap, err := ctx.GetStub().GetTransient()
	if err != nil {
		return "", fmt.Errorf("error getting transient: %v", err)
	}

	BidJSON, ok := transientMap["bid"]
	if !ok {
		return "", fmt.Errorf("bid key not found in the transient map")
	}

	// 获取私有数据集
	collection, err := getCollectionName(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get implicit collection name: %v", err)
	}

	// 验证peer节点并存储bid
	err = verifyClientOrgMatchesPeerOrg(ctx)
	if err != nil {
		return "", fmt.Errorf("Cannot store bid on this peer, not a member of this org: Error %v", err)
	}

	// txID 作为bid的一个标识
	txID := ctx.GetStub().GetTxID()

	// 用txID生成一个密钥，作为之后佩德森承诺生成过程中椭圆曲线的密钥参数
	bidKey, err := ctx.GetStub().CreateCompositeKey(bidKeyType, []string{auctionID, txID})
	if err != nil {
		return "", fmt.Errorf("failed to create composite key: %v", err)
	}

	// 将bid放入org的私有数据集中
	err = ctx.GetStub().PutPrivateData(collection, bidKey, BidJSON)
	if err != nil {
		return "", fmt.Errorf("failed to input price into collection: %v", err)
	}

	return txID, nil
}

// SubmitBid将私有数据集中的bid的佩德森承诺添加到拍卖中
func (s *SmartContract) SubmitBid(ctx contractapi.TransactionContextInterface, auctionID string, txID string) error {

	// 获取报价者组织的MSP ID
	clientOrgID, err := ctx.GetClientIdentity().GetMSPID()
	if err != nil {
		return fmt.Errorf("failed to get client MSP ID: %v", err)
	}

	// 从链上获取拍卖
	auction, err := s.QueryAuction(ctx,auctionID)
	if err != nil {
		return fmt.Errorf("failed to get auction from public state %v", err)
	}

	// 检查拍卖状态为open，否则不能提交报价
	Status := auction.Status
	if Status != "open" {
		return fmt.Errorf("cannot join closed or ended auction")
	}

	// 获取报价者所在组织的私有数据集
	collection, err := getCollectionName(ctx)
	if err != nil {
		return fmt.Errorf("failed to get implicit collection name: %v", err)
	}

	// 利用拍卖的ID和交易ID作为变量为佩德森承诺生成一个椭圆曲线群密钥
	bidKey, err := ctx.GetStub().NewECPrimeGroupKey(bidKeyType, []string{auctionID, txID})
	if err != nil {
		return fmt.Errorf("failed to create EC key: %v", err)
	}

	// 用生成的密钥为需要提交的报价值生成一个佩德森承诺
	bidCommitment, err := ctx.GetStub().VectorPCommit(collection, bidKey)
	if err != nil {
		return fmt.Errorf("failed to read bid bash from collection: %v", err)
	}

	// 将报价的佩德森承诺值添加到报价者所在组织的私有数据集中
	NewCommitment := bidCommitment{
		Org:  clientOrgID,
		Commitment: fmt.Sprintf("%x", bidCommitment),
	}

	bidders := make(map[string]BidCommitment)
	bidders = auction.PrivateBids
	bidders[bidKey] = NewCommitment
	auction.PrivateBids = bidders

	// 如果该报价者所在组织没有在拍卖的背书组织集中，将其添加进背书组织集
	Orgs := auction.Orgs
	if !(contains(Orgs, clientOrgID)) {
		newOrgs := append(Orgs, clientOrgID)
		auction.Orgs = newOrgs

		err = addAssetStateBasedEndorsement(ctx, auctionID, clientOrgID)
		if err != nil {
			return fmt.Errorf("failed setting state based endorsement for new organization: %v", err)
		}
	}

	newAuctionJSON, _ := json.Marshal(auction)

	err = ctx.GetStub().PutState (auctionID, newAuctionJSON)
	if err != nil {
		return fmt.Errorf("failed to update auction: %v", err)
	}

	return nil
}

// RevealBid 是在拍卖状态转换为closed之后，揭露报价
func (s *SmartContract) RevealBid(ctx contractapi.TransactionContextInterface, auctionID string, txID string) error {

	// 从transient map中获取bid
	transientMap, err := ctx.GetStub().GetTransient()
	if err != nil {
		return fmt.Errorf("error getting transient: %v", err)
	}

	transientBidJSON, ok := transientMap["bid"]
	if !ok {
		return fmt.Errorf("bid key not found in the transient map")
	}

	// 获取私有数据集
	collection, err := getCollectionName(ctx)
	if err != nil {
		return fmt.Errorf("failed to get implicit collection name: %v", err)
	}

	// 利用transaction ID生成密钥
	bidKey, err := ctx.GetStub().NewECPrimeGroupKey(bidKeyType, []string{auctionID, txID})
	if err != nil {
		return fmt.Errorf("failed to create EC prime group key: %v", err)
	}

	// 从公共账本上获取bid的承诺值
	bidHash, err := ctx.GetStub().VectorPCommit(collection, bidKey)
	if err != nil {
		return fmt.Errorf("failed to read pedersen commitment from collection: %v", err)
	}
	if bidCommitment == nil {
		return fmt.Errorf("bid commitment does not exist: %s", bidKey)
	}

	// 从链上获取拍卖
	auction, err := s.QueryAuction(ctx,auctionID)
	if err != nil {
		return fmt.Errorf("failed to get auction from public state %v", err)
	}

		// 拍卖仅仅能够被seller关闭

	// 获取提交交易用户的ID
	clientID, err := s.GetSubmittingClientIdentity(ctx)
	if err != nil {
		return fmt.Errorf("failed to get client identity %v", err)
	}

	Seller := auction.Seller
	if Seller != clientID {
		return fmt.Errorf("bids can only be revealed by seller: %v", err)
	}

	//进行四步check，三次检查通过后才能揭露报价
	
	// check 1: 检查拍卖状态为closed，用户无法再向拍卖提交报价
	Status := auction.Status
	if Status != "closed" {
		return fmt.Errorf("cannot reveal bid for open or ended auction")
	}

	// check 2: 检查一下佩德森承诺值是否跟公共账本上的承诺值相同（保证提交的是真实值）
	commitment := ec.New()
	commitment.Write(transientBidJSON)
	calculatedBidJSONCommitment := commitment.Sum(nil)

	if !bytes.Equal(calculatedBidJSONCommitment, bidCommitment) {
		return fmt.Errorf("commitment %x for bid JSON %s does not match commitment in ledger: %x, bidder is not real",
			calculatedBidJSONCommitment,
			transientBidJSON,
			bidCommitment,
		)
	}

	// check 3：验证报价的承诺值和起初提交的承诺值相等（保证在拍卖过程中，报价没有被修改过）
	bidders := auction.PrivateBids
	privateBidCommitmentString := bidders[bidKey].Commitment

	onChainBidCCommitmentString := fmt.Sprintf("%x", bidCommitment)
	if privateBidCommitmentString != onChainBidCommitmentString {
		return fmt.Errorf("commitment %s for bid JSON %s does not match commitment in auction: %s, bidder must have changed bid",
			privateBidCommitmentString,
			transientBidJSON,
			onChainBidCommitmentString,
		)
	
	// check 4:	对承诺值用bulletproofs零知识证明实现范围证明，保证其值合法(不会凭空产生资产)
	if ！RPVerify(RPProve(bidCommitment)) {

		t.Error("*****Range Proof FAILURE")
		fmt.Printf("Bid Commitment Value: %s", ran.String())
	}

	// 四次check都通过后，就将bid添加到拍卖中
	type transientBidInput struct {
		Price    int    `json:"price"`
		Org      string `json:"org"`
		Bidder   string `json:"bidder"`
	}

	// unmarshal bid input
	var bidInput transientBidInput
	err = json.Unmarshal(transientBidJSON, &bidInput)
	if err != nil {
		return fmt.Errorf("failed to unmarshal JSON: %v", err)
	}

	// 获取提交交易的用户ID
	clientID, err := s.GetSubmittingClientIdentity(ctx)
	if err != nil {
		return fmt.Errorf("failed to get client identity %v", err)
	}

	// 将transient map中的临时变量以及org ID存到bid的数据中
	NewBid := FullBid{
		Type:     bidKeyType,
		Price:    bidInput.Price,
		Org:      bidInput.Org,
		Bidder:   bidInput.Bidder,
	}

	// 保证该交易是由报价者本人提交的
	if bidInput.Bidder != clientID {
		return fmt.Errorf("Permission denied, client id %v is not the owner of the bid", clientID)
	}

	revealedBids := make(map[string]FullBid)
	revealedBids = auction.RevealedBids
	revealedBids[bidKey] = NewBid
	auction.RevealedBids = revealedBids

	newAuctionJSON, _ := json.Marshal(auction)

	// 更新链状态
	err = ctx.GetStub().PutState(auctionID, newAuctionJSON)
	if err != nil {
		return fmt.Errorf("failed to update auction: %v", err)
	}

	return nil
}

// CloseAuction 仅可以被seller调用来关闭拍卖 
func (s *SmartContract) CloseAuction(ctx contractapi.TransactionContextInterface, auctionID string) error {

	// 从链上获取拍卖
	auction, err := s.QueryAuction(ctx,auctionID)
	if err != nil {
		return fmt.Errorf("failed to get auction from public state %v", err)
	}

	// 访问控制（仅seller）

	// 获取提交交易的用户ID
	clientID, err := s.GetSubmittingClientIdentity(ctx)
	if err != nil {
		return fmt.Errorf("failed to get client identity %v", err)
	}

	Seller := auction.Seller
	if Seller != clientID {
		return fmt.Errorf("auction can only be closed by seller: %v", err)
	}

	Status := auction.Status
	if Status != "open" {
		return fmt.Errorf("cannot close auction that is not open")
	}

	auction.Status = string("closed")

	closedAuctionJSON, _ := json.Marshal(auction)

	err = ctx.GetStub().PutState(auctionID, closedAuctionJSON)
	if err != nil {
		return fmt.Errorf("failed to close auction: %v", err)
	}

	return nil
}

// EndAuction 用于结束拍卖以及计算拍卖赢家
func (s *SmartContract) EndAuction(ctx contractapi.TransactionContextInterface, auctionID string) error {

	// 从链上获取拍卖
	auction, err := s.QueryAuction(ctx,auctionID)
	if err != nil {
		return fmt.Errorf("failed to get auction from public state %v", err)
	}

	// 访问控制（仅seller）

	// 获取提交交易的用户ID
	clientID, err := s.GetSubmittingClientIdentity(ctx)
	if err != nil {
		return fmt.Errorf("failed to get client identity %v", err)
	}

	Seller := auction.Seller
	if Seller != clientID {
		return fmt.Errorf("auction can only be ended by seller: %v", err)
	}

	Status := auction.Status
	if Status != "closed" {
		return fmt.Errorf("Can only end a closed auction")
	}

	// 获取revealed bids列表
	revealedBidMap := auction.RevealedBids
	if len(auction.RevealedBids) == 0 {
		return fmt.Errorf("No bids have been revealed, cannot end auction: %v", err)
	}

	// 确定报价最高的赢家
	for _, bid := range revealedBidMap {
		if bid.Price > auction.Price {
			auction.Winner = bid.Bidder
			auction.Price = bid.Price
		}
	}

	// 检查是否还有报价比上一步决定出的赢家报价更高，若有则返回错误
	err = checkForHigherBid(ctx, auction.Price, auction.RevealedBids, auction.PrivateBids)
	if err != nil {
		return fmt.Errorf("Cannot end auction: %v", err)
	}

	auction.Status = string("ended")

	endedAuctionJSON, _ := json.Marshal(auction)

	err = ctx.GetStub().PutState(auctionID, endedAuctionJSON)
	if err != nil {
		return fmt.Errorf("failed to end auction: %v", err)
	}
	return nil
}
