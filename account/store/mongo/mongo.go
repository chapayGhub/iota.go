package mongo

import (
	"context"
	"github.com/iotaledger/iota.go/account/store"
	"github.com/iotaledger/iota.go/trinary"
	"github.com/mongodb/mongo-go-driver/bson"
	"github.com/mongodb/mongo-go-driver/mongo"
	"github.com/mongodb/mongo-go-driver/mongo/options"
	"github.com/mongodb/mongo-go-driver/mongo/readconcern"
	"github.com/mongodb/mongo-go-driver/mongo/writeconcern"
	"strconv"
	"time"
)

// ContextProviderFunc which generates a new context.
type ContextProviderFunc func() context.Context

// Config defines settings which are used in conjunction with MongoDB.
type Config struct {
	// The name of the database inside MongoDB.
	DBName string
	// The name of the collection in which to store accounts.
	CollName string
	// The context provider which gets called up on each MongoDB call
	// in order to determine the timeout/cancellation.
	ContextProvider ContextProviderFunc
}

const DefaultDBName = "iota_account"
const DefaultCollName = "accounts"

func defaultConfig(cnf *Config) *Config {
	if cnf == nil {
		return &Config{
			DBName:          DefaultDBName,
			CollName:        DefaultCollName,
			ContextProvider: defaultCtxProvider,
		}
	}
	if cnf.DBName == "" {
		cnf.DBName = DefaultDBName
	}
	if cnf.CollName == "" {
		cnf.CollName = DefaultCollName
	}
	if cnf.ContextProvider == nil {
		cnf.ContextProvider = defaultCtxProvider
	}
	return cnf
}

func defaultMongoDBConf() []*options.ClientOptions {
	return []*options.ClientOptions{
		{
			WriteConcern: writeconcern.New(writeconcern.J(true), writeconcern.WMajority(), writeconcern.WTimeout(5*time.Second)),
			ReadConcern:  readconcern.Majority(),
		},
	}
}

func defaultCtxProvider() context.Context {
	ctx, _ := context.WithTimeout(context.Background(), 5*time.Second)
	return ctx
}

// NewMongoStore creates a new MongoDB store. If no MongoDB client options are defined,
// the client will be configured with a majority read/write concern, 5 sec. write concern timeout and journal write acknowledgement.
func NewMongoStore(uri string, cnf *Config, opts ...*options.ClientOptions) (*MongoStore, error) {
	if len(opts) == 0 {
		opts = defaultMongoDBConf()
	}
	client, err := mongo.NewClientWithOptions(uri, opts...)
	if err != nil {
		return nil, err
	}
	mongoStore := &MongoStore{client: client, cnf: defaultConfig(cnf)}
	if err := mongoStore.init(); err != nil {
		return nil, err
	}
	return mongoStore, nil
}

type MongoStore struct {
	client *mongo.Client
	coll   *mongo.Collection
	cnf    *Config
}

func (ms *MongoStore) init() error {
	if err := ms.client.Connect(ms.cnf.ContextProvider()); err != nil {
		return err
	}
	if err := ms.client.Ping(ms.cnf.ContextProvider(), nil); err != nil {
		return err
	}
	ms.coll = ms.client.Database(ms.cnf.DBName).Collection(ms.cnf.CollName)
	return nil
}

func newaccountstate() *accountstate {
	return &accountstate{
		DepositRequests:  make(map[string]*store.StoredDepositRequest),
		PendingTransfers: make(map[string]*store.PendingTransfer),
	}
}

// account state with deposit requests map adjusted to use string keys as
// the marshaller of the MongoDB lib can't marshal uint64 map keys.
type accountstate struct {
	ID               string                                 `bson:"_id"`
	KeyIndex         uint64                                 `json:"key_index" bson:"key_index"`
	DepositRequests  map[string]*store.StoredDepositRequest `json:"deposit_requests" bson:"deposit_requests"`
	PendingTransfers map[string]*store.PendingTransfer      `json:"pending_transfers" bson:"pending_transfers"`
}

// FIXME: remove once MongoDB driver knows how to unmarshal uint64 map keys
func (ac *accountstate) AccountState() *store.AccountState {
	state := &store.AccountState{}
	state.PendingTransfers = ac.PendingTransfers
	state.KeyIndex = ac.KeyIndex
	state.DepositRequests = map[uint64]*store.StoredDepositRequest{}
	for key, val := range ac.DepositRequests {
		keyNum, _ := strconv.ParseUint(key, 10, 64)
		state.DepositRequests[keyNum] = val
	}
	return state
}

func (ms *MongoStore) LoadAccount(id string) (*store.AccountState, error) {
	state := newaccountstate()
	cursor := ms.coll.FindOne(ms.cnf.ContextProvider(), bson.D{{"_id", id}})
	if err := cursor.Err(); err != nil {
		return nil, err
	}
	if err := cursor.Decode(state); err != nil {
		// return an empty account state object if there isn't
		// a previously stored account
		if err == mongo.ErrNoDocuments {
			// we build our own account object to allocate the obj inside the db.
			state.ID = id
			_, err := ms.coll.InsertOne(ms.cnf.ContextProvider(), state)
			if err != nil {
				return nil, err
			}
			return store.NewAccountState(), nil
		}
		return nil, err
	}
	return state.AccountState(), nil
}

func (ms *MongoStore) RemoveAccount(id string) error {
	delRes, err := ms.coll.DeleteOne(ms.cnf.ContextProvider(), bson.D{{"_id", id}})
	if err != nil {
		return err
	}
	if delRes.DeletedCount == 0 {
		return store.ErrAccountNotFound
	}
	return nil
}

type keyindex struct {
	KeyIndex uint64 `bson:"key_index"`
}

func (ms *MongoStore) ReadIndex(id string) (uint64, error) {
	opts := &options.FindOneOptions{
		Projection: bson.D{
			{"_id", 0},
			{"key_index", 1},
		},
	}
	res := ms.coll.FindOne(ms.cnf.ContextProvider(), bson.D{{"_id", id}}, opts)
	if res.Err() != nil {
		return 0, res.Err()
	}
	result := &keyindex{}
	if err := res.Decode(result); err != nil {
		return 0, err
	}
	return result.KeyIndex, nil
}

func (ms *MongoStore) WriteIndex(id string, index uint64) error {
	mutation := bson.D{{"$set", bson.D{{"key_index", index}}}}
	_, err := ms.coll.UpdateOne(ms.cnf.ContextProvider(), bson.D{{"_id", id}}, mutation)
	if err != nil {
		return err
	}
	return nil
}

func (ms *MongoStore) AddDepositRequest(id string, index uint64, depositRequest *store.StoredDepositRequest) error {
	indexStr := strconv.FormatUint(index, 10)
	mutation := bson.D{{"$set", bson.D{{"deposit_requests." + indexStr, depositRequest}}}}
	_, err := ms.coll.UpdateOne(ms.cnf.ContextProvider(), bson.D{{"_id", id}}, mutation)
	if err != nil {
		return err
	}
	return nil
}

func (ms *MongoStore) RemoveDepositRequest(id string, index uint64) error {
	indexStr := strconv.FormatUint(index, 10)
	mutation := bson.D{{"$unset", bson.D{{"deposit_requests." + indexStr, ""}}}}
	_, err := ms.coll.UpdateOne(ms.cnf.ContextProvider(), bson.D{{"_id", id}}, mutation)
	if err != nil {
		return err
	}
	return nil
}

type depositrequests struct {
	DepositRequests map[string]*store.StoredDepositRequest `bson:"deposit_requests"`
}

// FIXME: remove once MongoDB driver knows how to unmarshal uint64 map keys
func (dr *depositrequests) convert() map[uint64]*store.StoredDepositRequest {
	m := make(map[uint64]*store.StoredDepositRequest, len(dr.DepositRequests))
	for key, val := range dr.DepositRequests {
		keyNum, _ := strconv.ParseUint(key, 10, 64)
		m[keyNum] = val
	}
	return m
}

func (ms *MongoStore) GetDepositRequests(id string) (map[uint64]*store.StoredDepositRequest, error) {
	opts := &options.FindOneOptions{
		Projection: bson.D{
			{"_id", 0},
			{"deposit_requests", 1},
		},
	}
	res := ms.coll.FindOne(ms.cnf.ContextProvider(), bson.D{{"_id", id}}, opts)
	if res.Err() != nil {
		return nil, res.Err()
	}
	result := &depositrequests{}
	if err := res.Decode(result); err != nil {
		return nil, err
	}
	return result.convert(), nil
}

func (ms *MongoStore) AddPendingTransfer(id string, originTailTxHash trinary.Hash, bundleTrytes []trinary.Trytes, indices ...uint64) error {
	pendingTransfer := store.TrytesToPendingTransfer(bundleTrytes)
	pendingTransfer.Tails = append(pendingTransfer.Tails, originTailTxHash)
	mutation := bson.D{
		{"$set", bson.D{{"pending_transfers." + originTailTxHash, pendingTransfer}}},
	}
	if len(indices) > 0 {
		unsetMap := bson.M{}
		for _, index := range indices {
			indexStr := strconv.FormatUint(index, 10)
			unsetMap["deposit_requests."+indexStr] = ""
		}
		mutation = append(mutation, bson.E{"$unset", unsetMap})
	}
	_, err := ms.coll.UpdateOne(ms.cnf.ContextProvider(), bson.D{{"_id", id}}, mutation)
	if err != nil {
		return err
	}
	return nil
}

func (ms *MongoStore) RemovePendingTransfer(id string, originTailTxHash trinary.Hash) error {
	mutation := bson.D{{"$unset", bson.D{{"pending_transfers." + originTailTxHash, ""}}}}
	_, err := ms.coll.UpdateOne(ms.cnf.ContextProvider(), bson.D{{"_id", id}}, mutation)
	if err != nil {
		return err
	}
	return nil
}

func (ms *MongoStore) AddTailHash(id string, originTailTxHash trinary.Hash, newTailTxHash trinary.Hash) error {
	mutation := bson.D{
		{"$addToSet", bson.D{{"pending_transfers." + originTailTxHash + ".tails", newTailTxHash}}},
	}
	_, err := ms.coll.UpdateOne(ms.cnf.ContextProvider(), bson.D{
		{"_id", id},
		{"pending_transfers." + originTailTxHash, bson.D{{"$exists", true}}},
	}, mutation)
	if err != nil {
		return err
	}
	return nil
}

type pendingtransfers struct {
	PendingTransfers map[string]*store.PendingTransfer `bson:"pending_transfers"`
}

func (ms *MongoStore) GetPendingTransfers(id string) (map[string]*store.PendingTransfer, error) {
	opts := &options.FindOneOptions{
		Projection: bson.D{
			{"_id", 0},
			{"pending_transfers", 1},
		},
	}
	res := ms.coll.FindOne(ms.cnf.ContextProvider(), bson.D{{"_id", id}}, opts)
	if res.Err() != nil {
		return nil, res.Err()
	}
	result := &pendingtransfers{}
	if err := res.Decode(result); err != nil {
		return nil, err
	}
	return result.PendingTransfers, nil
}
