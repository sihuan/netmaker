package controller

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/gorilla/mux"
	"github.com/gravitl/netmaker/functions"
	"github.com/gravitl/netmaker/models"
	"github.com/gravitl/netmaker/mongoconn"
	"github.com/gravitl/netmaker/servercfg"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const ALL_NETWORK_ACCESS = "THIS_USER_HAS_ALL"
const NO_NETWORKS_PRESENT = "THIS_USER_HAS_NONE"

func networkHandlers(r *mux.Router) {
	r.HandleFunc("/api/networks", securityCheck(false, http.HandlerFunc(getNetworks))).Methods("GET")
	r.HandleFunc("/api/networks", securityCheck(true, http.HandlerFunc(createNetwork))).Methods("POST")
	r.HandleFunc("/api/networks/{networkname}", securityCheck(false, http.HandlerFunc(getNetwork))).Methods("GET")
	r.HandleFunc("/api/networks/{networkname}", securityCheck(false, http.HandlerFunc(updateNetwork))).Methods("PUT")
	r.HandleFunc("/api/networks/{networkname}/nodelimit", securityCheck(true, http.HandlerFunc(updateNetworkNodeLimit))).Methods("PUT")
	r.HandleFunc("/api/networks/{networkname}", securityCheck(true, http.HandlerFunc(deleteNetwork))).Methods("DELETE")
	r.HandleFunc("/api/networks/{networkname}/keyupdate", securityCheck(false, http.HandlerFunc(keyUpdate))).Methods("POST")
	r.HandleFunc("/api/networks/{networkname}/keys", securityCheck(false, http.HandlerFunc(createAccessKey))).Methods("POST")
	r.HandleFunc("/api/networks/{networkname}/keys", securityCheck(false, http.HandlerFunc(getAccessKeys))).Methods("GET")
	r.HandleFunc("/api/networks/{networkname}/signuptoken", securityCheck(false, http.HandlerFunc(getSignupToken))).Methods("GET")
	r.HandleFunc("/api/networks/{networkname}/keys/{name}", securityCheck(false, http.HandlerFunc(deleteAccessKey))).Methods("DELETE")
}

//Security check is middleware for every function and just checks to make sure that its the master calling
//Only admin should have access to all these network-level actions
//or maybe some Users once implemented
func securityCheck(reqAdmin bool, next http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var errorResponse = models.ErrorResponse{
			Code: http.StatusUnauthorized, Message: "W1R3: It's not you it's me.",
		}

		var params = mux.Vars(r)
		bearerToken := r.Header.Get("Authorization")
		err, networks, username := SecurityCheck(reqAdmin, params["networkname"], bearerToken)
		if err != nil {
			if strings.Contains(err.Error(), "does not exist") {
				errorResponse.Code = http.StatusNotFound
			}
			errorResponse.Message = err.Error()
			returnErrorResponse(w, r, errorResponse)
			return
		}
		networksJson, err := json.Marshal(&networks)
		if err != nil {
			errorResponse.Message = err.Error()
			returnErrorResponse(w, r, errorResponse)
			return
		}
		r.Header.Set("user", username)
		r.Header.Set("networks", string(networksJson))
		next.ServeHTTP(w, r)
	}
}

func SecurityCheck(reqAdmin bool, netname, token string) (error, []string, string) {

	networkexists, err := functions.NetworkExists(netname)
	if err != nil {
		return err, nil, ""
	}
	if netname != "" && !networkexists {
		return errors.New("This network does not exist"), nil, ""
	}

	var hasBearer = true
	var tokenSplit = strings.Split(token, " ")
	var authToken = ""

	if len(tokenSplit) < 2 {
		hasBearer = false
	} else {
		authToken = tokenSplit[1]
	}
	userNetworks := []string{}
	//all endpoints here require master so not as complicated
	isMasterAuthenticated := authenticateMaster(authToken)
	username := ""
	if !hasBearer || !isMasterAuthenticated {
		userName, networks, isadmin, err := functions.VerifyUserToken(authToken)
		username = userName
		if err != nil {
			return errors.New("Error verifying user token"), nil, username
		}
		if !isadmin && reqAdmin {
			return errors.New("You are unauthorized to access this endpoint"), nil, username
		}
		userNetworks = networks
		if isadmin {
			userNetworks = []string{ALL_NETWORK_ACCESS}
		}
	} else if isMasterAuthenticated {
		userNetworks = []string{ALL_NETWORK_ACCESS}
	}
	if len(userNetworks) == 0 {
		userNetworks = append(userNetworks, NO_NETWORKS_PRESENT)
	}
	return nil, userNetworks, username
}

//Consider a more secure way of setting master key
func authenticateMaster(tokenString string) bool {
	if tokenString == servercfg.GetMasterKey() {
		return true
	}
	return false
}

//simple get all networks function
func getNetworks(w http.ResponseWriter, r *http.Request) {

	headerNetworks := r.Header.Get("networks")
	networksSlice := []string{}
	marshalErr := json.Unmarshal([]byte(headerNetworks), &networksSlice)
	if marshalErr != nil {
		returnErrorResponse(w, r, formatError(marshalErr, "internal"))
		return
	}
	allnetworks := []models.Network{}
	err := errors.New("Networks Error")
	if networksSlice[0] == ALL_NETWORK_ACCESS {
		allnetworks, err = functions.ListNetworks()
		if err != nil {
			returnErrorResponse(w, r, formatError(err, "internal"))
			return
		}
	} else {
		for _, network := range networksSlice {
			netObject, parentErr := functions.GetParentNetwork(network)
			if parentErr == nil {
				allnetworks = append(allnetworks, netObject)
			}
		}
	}
	networks := RemoveComms(allnetworks)
	functions.PrintUserLog(r.Header.Get("user"), "fetched networks.", 2)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(networks)
}

func RemoveComms(networks []models.Network) []models.Network {
	var index int = 100000001
	for ind, net := range networks {
		if net.NetID == "comms" {
			index = ind
		}
	}
	if index == 100000001 {
		return networks
	}
	returnable := make([]models.Network, 0)
	returnable = append(returnable, networks[:index]...)
	return append(returnable, networks[index+1:]...)
}

func ValidateNetworkUpdate(network models.NetworkUpdate) error {
	v := validator.New()

	_ = v.RegisterValidation("netid_valid", func(fl validator.FieldLevel) bool {
		if fl.Field().String() == "" {
			return true
		}
		inCharSet := functions.NameInNetworkCharSet(fl.Field().String())
		return inCharSet
	})

	//	_ = v.RegisterValidation("addressrange_valid", func(fl validator.FieldLevel) bool {
	//		isvalid := fl.Field().String() == "" || functions.IsIpCIDR(fl.Field().String())
	//		return isvalid
	//	})
	//_ = v.RegisterValidation("addressrange6_valid", func(fl validator.FieldLevel) bool {
	//		isvalid := fl.Field().String() == "" || functions.IsIpCIDR(fl.Field().String())
	//		return isvalid
	//	})

	//	_ = v.RegisterValidation("localrange_valid", func(fl validator.FieldLevel) bool {
	//		isvalid := fl.Field().String() == "" || functions.IsIpCIDR(fl.Field().String())
	//		return isvalid
	//	})

	//	_ = v.RegisterValidation("netid_valid", func(fl validator.FieldLevel) bool {
	//		return true
	//	})

	//	_ = v.RegisterValidation("displayname_unique", func(fl validator.FieldLevel) bool {
	//		return true
	//	})

	err := v.Struct(network)

	if err != nil {
		for _, e := range err.(validator.ValidationErrors) {
			fmt.Println(e)
		}
	}
	return err
}

func ValidateNetworkCreate(network models.Network) error {

	v := validator.New()

	//	_ = v.RegisterValidation("addressrange_valid", func(fl validator.FieldLevel) bool {
	//		isvalid := functions.IsIpCIDR(fl.Field().String())
	//		return isvalid
	//	})
	_ = v.RegisterValidation("addressrange6_valid", func(fl validator.FieldLevel) bool {
		isvalid := true
		if *network.IsDualStack {
			isvalid = functions.IsIpCIDR(fl.Field().String())
		}
		return isvalid
	})
	//
	//	_ = v.RegisterValidation("localrange_valid", func(fl validator.FieldLevel) bool {
	//		isvalid := fl.Field().String() == "" || functions.IsIpCIDR(fl.Field().String())
	//		return isvalid
	//	})
	//
	_ = v.RegisterValidation("netid_valid", func(fl validator.FieldLevel) bool {
		isFieldUnique, _ := functions.IsNetworkNameUnique(fl.Field().String())
		inCharSet := functions.NameInNetworkCharSet(fl.Field().String())
		return isFieldUnique && inCharSet
	})
	//
	_ = v.RegisterValidation("displayname_valid", func(fl validator.FieldLevel) bool {
		isFieldUnique, _ := functions.IsNetworkDisplayNameUnique(fl.Field().String())
		inCharSet := functions.NameInNetworkCharSet(fl.Field().String())
		return isFieldUnique && inCharSet
	})

	err := v.Struct(network)

	if err != nil {
		for _, e := range err.(validator.ValidationErrors) {
			fmt.Println(e)
		}
	}
	return err
}

//Simple get network function
func getNetwork(w http.ResponseWriter, r *http.Request) {
	// set header.
	w.Header().Set("Content-Type", "application/json")
	var params = mux.Vars(r)
	netname := params["networkname"]
	network, err := GetNetwork(netname)
	if err != nil {
		returnErrorResponse(w, r, formatError(err, "internal"))
		return
	}
	functions.PrintUserLog(r.Header.Get("user"), "fetched network "+netname, 2)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(network)
}

func GetNetwork(name string) (models.Network, error) {
	var network models.Network
	collection := mongoconn.Client.Database("netmaker").Collection("networks")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	filter := bson.M{"netid": name}
	err := collection.FindOne(ctx, filter, options.FindOne().SetProjection(bson.M{"_id": 0})).Decode(&network)
	defer cancel()
	if err != nil {
		return models.Network{}, err
	}
	return network, nil
}

func keyUpdate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var params = mux.Vars(r)
	netname := params["networkname"]
	network, err := KeyUpdate(netname)
	if err != nil {
		returnErrorResponse(w, r, formatError(err, "internal"))
		return
	}
	functions.PrintUserLog(r.Header.Get("user"), "updated key on network "+netname, 2)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(network)
}

func KeyUpdate(netname string) (models.Network, error) {
	network, err := functions.GetParentNetwork(netname)
	if err != nil {
		return models.Network{}, err
	}
	network.KeyUpdateTimeStamp = time.Now().Unix()
	collection := mongoconn.Client.Database("netmaker").Collection("networks")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	filter := bson.M{"netid": netname}
	// prepare update model.
	update := bson.D{
		{"$set", bson.D{
			{"addressrange", network.AddressRange},
			{"addressrange6", network.AddressRange6},
			{"displayname", network.DisplayName},
			{"defaultlistenport", network.DefaultListenPort},
			{"defaultpostup", network.DefaultPostUp},
			{"defaultpostdown", network.DefaultPostDown},
			{"defaultkeepalive", network.DefaultKeepalive},
			{"keyupdatetimestamp", network.KeyUpdateTimeStamp},
			{"defaultsaveconfig", network.DefaultSaveConfig},
			{"defaultinterface", network.DefaultInterface},
			{"nodeslastmodified", network.NodesLastModified},
			{"networklastmodified", network.NetworkLastModified},
			{"allowmanualsignup", network.AllowManualSignUp},
			{"checkininterval", network.DefaultCheckInInterval},
		}},
	}
	err = collection.FindOneAndUpdate(ctx, filter, update).Decode(&network)
	defer cancel()
	if err != nil {
		return models.Network{}, err
	}
	return network, nil
}

//Update a network
func AlertNetwork(netid string) error {

	collection := mongoconn.Client.Database("netmaker").Collection("networks")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	filter := bson.M{"netid": netid}

	var network models.Network

	network, err := functions.GetParentNetwork(netid)
	if err != nil {
		return err
	}
	updatetime := time.Now().Unix()
	update := bson.D{
		{"$set", bson.D{
			{"nodeslastmodified", updatetime},
			{"networklastmodified", updatetime},
		}},
	}

	err = collection.FindOneAndUpdate(ctx, filter, update).Decode(&network)
	defer cancel()

	return err
}

//Update a network
func updateNetwork(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var params = mux.Vars(r)
	var network models.Network
	netname := params["networkname"]
	network, err := functions.GetParentNetwork(netname)
	if err != nil {
		returnErrorResponse(w, r, formatError(err, "internal"))
		return
	}

	var networkChange models.NetworkUpdate

	_ = json.NewDecoder(r.Body).Decode(&networkChange)
	if networkChange.AddressRange == "" {
		networkChange.AddressRange = network.AddressRange
	}
	if networkChange.AddressRange6 == "" {
		networkChange.AddressRange6 = network.AddressRange6
	}
	if networkChange.NetID == "" {
		networkChange.NetID = network.NetID
	}

	err = ValidateNetworkUpdate(networkChange)
	if err != nil {
		returnErrorResponse(w, r, formatError(err, "badrequest"))
		return
	}
	returnednetwork, err := UpdateNetwork(networkChange, network)
	if err != nil {
		returnErrorResponse(w, r, formatError(err, "badrequest"))
		return
	}

	functions.PrintUserLog(r.Header.Get("user"), "updated network "+netname, 1)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(returnednetwork)
}

func updateNetworkNodeLimit(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var params = mux.Vars(r)
	var network models.Network
	netname := params["networkname"]
	network, err := functions.GetParentNetwork(netname)
	if err != nil {
		returnErrorResponse(w, r, formatError(err, "internal"))
		return
	}

	var networkChange models.NetworkUpdate

	_ = json.NewDecoder(r.Body).Decode(&networkChange)

	collection := mongoconn.Client.Database("netmaker").Collection("networks")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	filter := bson.M{"netid": network.NetID}

	if networkChange.NodeLimit != 0 {
		update := bson.D{
			{"$set", bson.D{
				{"nodelimit", networkChange.NodeLimit},
			}},
		}
		err := collection.FindOneAndUpdate(ctx, filter, update).Decode(&network)
		defer cancel()
		if err != nil {
			returnErrorResponse(w, r, formatError(err, "badrequest"))
			return
		}
	}
	functions.PrintUserLog(r.Header.Get("user"), "updated network node limit on, "+netname, 1)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(network)
}

func UpdateNetwork(networkChange models.NetworkUpdate, network models.Network) (models.Network, error) {
	//NOTE: Network.NetID is intentionally NOT editable. It acts as a static ID for the network.
	//DisplayName can be changed instead, which is what shows on the front end
	if networkChange.NetID != network.NetID {
		return models.Network{}, errors.New("NetID is not editable")
	}

	haschange := false
	hasrangeupdate := false
	haslocalrangeupdate := false

	if networkChange.AddressRange != "" {
		haschange = true
		hasrangeupdate = true
		network.AddressRange = networkChange.AddressRange
	}
	if networkChange.LocalRange != "" {
		haschange = true
		haslocalrangeupdate = true
		network.LocalRange = networkChange.LocalRange
	}
	if networkChange.IsLocal != nil {
		network.IsLocal = networkChange.IsLocal
	}
	if networkChange.IsDualStack != nil {
		network.IsDualStack = networkChange.IsDualStack
	}
	if networkChange.DefaultListenPort != 0 {
		network.DefaultListenPort = networkChange.DefaultListenPort
		haschange = true
	}
	if networkChange.DefaultPostDown != "" {
		network.DefaultPostDown = networkChange.DefaultPostDown
		haschange = true
	}
	if networkChange.DefaultInterface != "" {
		network.DefaultInterface = networkChange.DefaultInterface
		haschange = true
	}
	if networkChange.DefaultPostUp != "" {
		network.DefaultPostUp = networkChange.DefaultPostUp
		haschange = true
	}
	if networkChange.DefaultKeepalive != 0 {
		network.DefaultKeepalive = networkChange.DefaultKeepalive
		haschange = true
	}
	if networkChange.DisplayName != "" {
		network.DisplayName = networkChange.DisplayName
		haschange = true
	}
	if networkChange.DefaultCheckInInterval != 0 {
		network.DefaultCheckInInterval = networkChange.DefaultCheckInInterval
		haschange = true
	}
	if networkChange.AllowManualSignUp != nil {
		network.AllowManualSignUp = networkChange.AllowManualSignUp
		haschange = true
	}

	collection := mongoconn.Client.Database("netmaker").Collection("networks")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	filter := bson.M{"netid": network.NetID}

	if haschange {
		network.SetNetworkLastModified()
	}
	// prepare update model.
	update := bson.D{
		{"$set", bson.D{
			{"addressrange", network.AddressRange},
			{"addressrange6", network.AddressRange6},
			{"displayname", network.DisplayName},
			{"defaultlistenport", network.DefaultListenPort},
			{"defaultpostup", network.DefaultPostUp},
			{"defaultpostdown", network.DefaultPostDown},
			{"defaultkeepalive", network.DefaultKeepalive},
			{"defaultsaveconfig", network.DefaultSaveConfig},
			{"defaultinterface", network.DefaultInterface},
			{"nodeslastmodified", network.NodesLastModified},
			{"networklastmodified", network.NetworkLastModified},
			{"allowmanualsignup", network.AllowManualSignUp},
			{"localrange", network.LocalRange},
			{"islocal", network.IsLocal},
			{"isdualstack", network.IsDualStack},
			{"checkininterval", network.DefaultCheckInInterval},
		}},
	}

	err := collection.FindOneAndUpdate(ctx, filter, update).Decode(&network)
	defer cancel()

	if err != nil {
		return models.Network{}, err
	}

	//Cycles through nodes and gives them new IP's based on the new range
	//Pretty cool, but also pretty inefficient currently
	if hasrangeupdate {
		err = functions.UpdateNetworkNodeAddresses(network.NetID)
		if err != nil {
			return models.Network{}, err
		}
	}
	if haslocalrangeupdate {
		err = functions.UpdateNetworkLocalAddresses(network.NetID)
		if err != nil {
			return models.Network{}, err
		}
	}
	returnnetwork, err := functions.GetParentNetwork(network.NetID)
	if err != nil {
		return models.Network{}, err
	}
	return returnnetwork, nil
}

//Delete a network
//Will stop you if  there's any nodes associated
func deleteNetwork(w http.ResponseWriter, r *http.Request) {
	// Set header
	w.Header().Set("Content-Type", "application/json")

	var params = mux.Vars(r)
	network := params["networkname"]
	count, err := DeleteNetwork(network)

	if err != nil {
		errtype := "badrequest"
		if strings.Contains(err.Error(), "Node check failed") {
			errtype = "forbidden"
		}
		returnErrorResponse(w, r, formatError(err, errtype))
		return
	}
	functions.PrintUserLog(r.Header.Get("user"), "deleted network "+network, 1)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(count)
}

func DeleteNetwork(network string) (*mongo.DeleteResult, error) {
	none := &mongo.DeleteResult{}

	nodecount, err := functions.GetNetworkNodeNumber(network)
	if err != nil {
		//returnErrorResponse(w, r, formatError(err, "internal"))
		return none, err
	} else if nodecount > 0 {
		//errorResponse := models.ErrorResponse{
		//	Code: http.StatusForbidden, Message: "W1R3: Node check failed. All nodes must be deleted before deleting network.",
		//}
		//returnErrorResponse(w, r, errorResponse)
		return none, errors.New("Node check failed. All nodes must be deleted before deleting network")
	}

	collection := mongoconn.Client.Database("netmaker").Collection("networks")
	filter := bson.M{"netid": network}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	deleteResult, err := collection.DeleteOne(ctx, filter)
	defer cancel()
	if err != nil {
		//returnErrorResponse(w, r, formatError(err, "internal"))
		return none, err
	}
	return deleteResult, nil
}

//Create a network
//Pretty simple
func createNetwork(w http.ResponseWriter, r *http.Request) {

	w.Header().Set("Content-Type", "application/json")

	var network models.Network

	// we decode our body request params
	err := json.NewDecoder(r.Body).Decode(&network)
	if err != nil {
		returnErrorResponse(w, r, formatError(err, "internal"))
		return
	}

	err = CreateNetwork(network)
	if err != nil {
		returnErrorResponse(w, r, formatError(err, "badrequest"))
		return
	}
	functions.PrintUserLog(r.Header.Get("user"), "created network "+network.NetID, 1)
	w.WriteHeader(http.StatusOK)
	//json.NewEncoder(w).Encode(result)
}

func CreateNetwork(network models.Network) error {
	//TODO: Not really doing good validation here. Same as createNode, updateNode, and updateNetwork
	//Need to implement some better validation across the board

	if network.IsLocal == nil {
		falsevar := false
		network.IsLocal = &falsevar
	}
	if network.IsDualStack == nil {
		falsevar := false
		network.IsDualStack = &falsevar
	}

	err := ValidateNetworkCreate(network)
	if err != nil {
		//returnErrorResponse(w, r, formatError(err, "badrequest"))
		return err
	}
	network.SetDefaults()
	network.SetNodesLastModified()
	network.SetNetworkLastModified()
	network.KeyUpdateTimeStamp = time.Now().Unix()

	collection := mongoconn.Client.Database("netmaker").Collection("networks")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)

	// insert our network into the network table
	_, err = collection.InsertOne(ctx, network)
	defer cancel()
	if err != nil {
		return err
	}
	return nil
}

// BEGIN KEY MANAGEMENT SECTION

//TODO: Very little error handling
//accesskey is created as a json string inside the Network collection item in mongo
func createAccessKey(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var params = mux.Vars(r)
	var accesskey models.AccessKey
	//start here
	netname := params["networkname"]
	network, err := functions.GetParentNetwork(netname)
	if err != nil {
		returnErrorResponse(w, r, formatError(err, "internal"))
		return
	}
	err = json.NewDecoder(r.Body).Decode(&accesskey)
	if err != nil {
		returnErrorResponse(w, r, formatError(err, "internal"))
		return
	}
	key, err := CreateAccessKey(accesskey, network)
	if err != nil {
		returnErrorResponse(w, r, formatError(err, "badrequest"))
		return
	}
	functions.PrintUserLog(r.Header.Get("user"), "created access key "+netname, 1)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(key)
	//w.Write([]byte(accesskey.AccessString))
}

func CreateAccessKey(accesskey models.AccessKey, network models.Network) (models.AccessKey, error) {

	if accesskey.Name == "" {
		accesskey.Name = functions.GenKeyName()
	}

	if accesskey.Value == "" {
		accesskey.Value = functions.GenKey()
	}
	if accesskey.Uses == 0 {
		accesskey.Uses = 1
	}

	checkkeys, err := GetKeys(network.NetID)
	if err != nil {
		return models.AccessKey{}, errors.New("could not retrieve network keys")
	}

	for _, key := range checkkeys {
		if key.Name == accesskey.Name {
			return models.AccessKey{}, errors.New("Duplicate AccessKey Name")
		}
	}
	privAddr := ""
	if network.IsLocal != nil {
		if *network.IsLocal {
			privAddr = network.LocalRange
		}
	}

	netID := network.NetID

	var accessToken models.AccessToken
	s := servercfg.GetServerConfig()
	w := servercfg.GetWGConfig()
	servervals := models.ServerConfig{
		CoreDNSAddr:    s.CoreDNSAddr,
		APIConnString:  s.APIConnString,
		APIHost:        s.APIHost,
		APIPort:        s.APIPort,
		GRPCConnString: s.GRPCConnString,
		GRPCHost:       s.GRPCHost,
		GRPCPort:       s.GRPCPort,
		GRPCSSL:        s.GRPCSSL,
	}
	wgvals := models.WG{
		GRPCWireGuard:  w.GRPCWireGuard,
		GRPCWGAddress:  w.GRPCWGAddress,
		GRPCWGPort:     w.GRPCWGPort,
		GRPCWGPubKey:   w.GRPCWGPubKey,
		GRPCWGEndpoint: s.APIHost,
	}

	accessToken.ServerConfig = servervals
	accessToken.WG = wgvals
	accessToken.ClientConfig.Network = netID
	accessToken.ClientConfig.Key = accesskey.Value
	accessToken.ClientConfig.LocalRange = privAddr

	tokenjson, err := json.Marshal(accessToken)
	if err != nil {
		return accesskey, err
	}

	accesskey.AccessString = base64.StdEncoding.EncodeToString([]byte(tokenjson))

	//validate accesskey
	v := validator.New()
	err = v.Struct(accesskey)
	if err != nil {
		for _, e := range err.(validator.ValidationErrors) {
			fmt.Println(e)
		}
		return models.AccessKey{}, err
	}
	network.AccessKeys = append(network.AccessKeys, accesskey)
	collection := mongoconn.Client.Database("netmaker").Collection("networks")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	// Create filter
	filter := bson.M{"netid": network.NetID}
	// Read update model from body request
	// prepare update model.
	update := bson.D{
		{"$set", bson.D{
			{"accesskeys", network.AccessKeys},
		}},
	}
	err = collection.FindOneAndUpdate(ctx, filter, update).Decode(&network)
	defer cancel()
	if err != nil {
		//returnErrorResponse(w, r, formatError(err, "internal"))
		return models.AccessKey{}, err
	}
	return accesskey, nil
}

func GetSignupToken(netID string) (models.AccessKey, error) {

	var accesskey models.AccessKey
	var accessToken models.AccessToken
	s := servercfg.GetServerConfig()
	w := servercfg.GetWGConfig()
	servervals := models.ServerConfig{
		APIConnString:  s.APIConnString,
		APIHost:        s.APIHost,
		APIPort:        s.APIPort,
		GRPCConnString: s.GRPCConnString,
		GRPCHost:       s.GRPCHost,
		GRPCPort:       s.GRPCPort,
		GRPCSSL:        s.GRPCSSL,
	}
	wgvals := models.WG{
		GRPCWireGuard:  w.GRPCWireGuard,
		GRPCWGAddress:  w.GRPCWGAddress,
		GRPCWGPort:     w.GRPCWGPort,
		GRPCWGPubKey:   w.GRPCWGPubKey,
		GRPCWGEndpoint: s.APIHost,
	}

	accessToken.ServerConfig = servervals
	accessToken.WG = wgvals

	tokenjson, err := json.Marshal(accessToken)
	if err != nil {
		return accesskey, err
	}

	accesskey.AccessString = base64.StdEncoding.EncodeToString([]byte(tokenjson))
	return accesskey, nil
}
func getSignupToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var params = mux.Vars(r)
	netID := params["networkname"]

	token, err := GetSignupToken(netID)
	if err != nil {
		returnErrorResponse(w, r, formatError(err, "internal"))
		return
	}
	functions.PrintUserLog(r.Header.Get("user"), "got signup token "+netID, 2)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(token)
}

//pretty simple get
func getAccessKeys(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var params = mux.Vars(r)
	network := params["networkname"]
	keys, err := GetKeys(network)
	if err != nil {
		returnErrorResponse(w, r, formatError(err, "internal"))
		return
	}
	functions.PrintUserLog(r.Header.Get("user"), "fetched access keys on network "+network, 2)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(keys)
}
func GetKeys(net string) ([]models.AccessKey, error) {

	var network models.Network
	collection := mongoconn.Client.Database("netmaker").Collection("networks")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	filter := bson.M{"netid": net}
	err := collection.FindOne(ctx, filter, options.FindOne().SetProjection(bson.M{"_id": 0})).Decode(&network)
	defer cancel()
	if err != nil {
		return []models.AccessKey{}, err
	}
	return network.AccessKeys, nil
}

//delete key. Has to do a little funky logic since it's not a collection item
func deleteAccessKey(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var params = mux.Vars(r)
	keyname := params["name"]
	netname := params["networkname"]
	err := DeleteKey(keyname, netname)
	if err != nil {
		returnErrorResponse(w, r, formatError(err, "badrequest"))
		return
	}
	functions.PrintUserLog(r.Header.Get("user"), "deleted access key "+keyname+" on network "+netname, 1)
	w.WriteHeader(http.StatusOK)
}
func DeleteKey(keyname, netname string) error {
	network, err := functions.GetParentNetwork(netname)
	if err != nil {
		return err
	}
	//basically, turn the list of access keys into the list of access keys before and after the item
	//have not done any error handling for if there's like...1 item. I think it works? need to test.
	found := false
	var updatedKeys []models.AccessKey
	for _, currentkey := range network.AccessKeys {
		if currentkey.Name == keyname {
			found = true
		} else {
			updatedKeys = append(updatedKeys, currentkey)
		}
	}
	if !found {
		return errors.New("key " + keyname + " does not exist")
	}

	collection := mongoconn.Client.Database("netmaker").Collection("networks")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	// Create filter
	filter := bson.M{"netid": netname}
	// prepare update model.
	update := bson.D{
		{"$set", bson.D{
			{"accesskeys", updatedKeys},
		}},
	}
	err = collection.FindOneAndUpdate(ctx, filter, update).Decode(&network)
	defer cancel()
	if err != nil {
		return err
	}
	return nil
}
