package network

import (
	"fmt"

	"strconv"
	"strings"

	"context"

	"github.com/SmartMeshFoundation/raiden-network/blockchain"
	"github.com/SmartMeshFoundation/raiden-network/network/rpc"
	"github.com/SmartMeshFoundation/raiden-network/params"
	"github.com/SmartMeshFoundation/raiden-network/utils"
	"github.com/astaxie/beego/httplib"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/log"
	"github.com/kataras/go-errors"
)

var hostportPrefix = []byte("hostport_")
var nodePrefix = []byte("node_")

type DiscoveryInterface interface {
	Register(address common.Address, host string, port int) error
	Get(address common.Address) (host string, port int, err error)
	NodeIdByHostPort(host string, port int) (node common.Address, err error)
}
type HttpDiscovery struct {
	Path string
}

func NewHttpDiscovery() *HttpDiscovery {
	return &HttpDiscovery{
		Path: "http://182.254.155.208:50000/public/",
	}
}
func (this *HttpDiscovery) Register(address common.Address, host string, port int) error {
	_, err := httplib.Get(fmt.Sprintf(this.Path+"register?addr=%s&hostport=%s", strings.ToLower(address.String()), fmt.Sprintf("%s:%d", host, port))).String()
	return err
}
func (this *HttpDiscovery) Get(address common.Address) (host string, port int, err error) {
	s, err := httplib.Get(fmt.Sprintf(this.Path+"gethostport?addr=%s", strings.ToLower(address.String()))).String()
	host, port = SplitHostPort(s)
	return
}

func (this *HttpDiscovery) NodeIdByHostPort(host string, port int) (node common.Address, err error) {
	s, err := httplib.Get(fmt.Sprintf(this.Path+"getaddr?hostport=%s", fmt.Sprintf("%s:%d", host, port))).String()
	if err != nil {
		return
	}
	node = common.HexToAddress(s)
	return
}

type Discovery struct {
	NodeIdHostPortMap map[common.Address]string
}

func NewDiscovery() *Discovery {
	return &Discovery{
		NodeIdHostPortMap: make(map[common.Address]string),
	}
}

func (this *Discovery) Register(address common.Address, host string, port int) error {
	this.NodeIdHostPortMap[address] = fmt.Sprintf("%s:%d", host, port)
	return nil
}

func SplitHostPort(hostport string) (string, int) {
	ss := strings.Split(hostport, ":")
	port, _ := strconv.Atoi(ss[1])
	return ss[0], port
}
func (this *Discovery) Get(address common.Address) (host string, port int, err error) {
	hostport, ok := this.NodeIdHostPortMap[address]
	if !ok {
		err = errors.New("no such address")
		return
	}
	host, port = SplitHostPort(hostport)
	return
}

func (this *Discovery) NodeIdByHostPort(host string, port int) (node common.Address, err error) {
	hostport := tohostport(host, port)
	for k, v := range this.NodeIdHostPortMap {
		if v == hostport {
			return k, nil
		}
	}
	return utils.EmptyAddress, errors.New("not found")
}

//Allows registering and looking up by endpoint (host, port) for node_address.
type ContractDiscovery struct {
	discovery   *rpc.EndpointRegistry
	client      *ethclient.Client
	auth        *bind.TransactOpts
	node        common.Address
	cacheAddr   map[common.Address]string
	cacheSocket map[string]common.Address
	//db          ethdb.Database
}

func NewContractDiscovery(mynode common.Address, client *ethclient.Client, auth *bind.TransactOpts) *ContractDiscovery {
	c := &ContractDiscovery{
		client:      client,
		auth:        auth,
		node:        mynode,
		cacheAddr:   make(map[common.Address]string),
		cacheSocket: make(map[string]common.Address),
	}
	c.discovery, _ = rpc.NewEndpointRegistry(params.ROPSTEN_DISCOVERY_ADDRESS, client)
	ch := make(chan types.Log, 1)
	_, err := rpc.EventSubscribe(params.ROPSTEN_DISCOVERY_ADDRESS, "AddressRegistered", rpc.EndpointRegistryABI, client, ch)
	if err == nil {
		//c.db = db.GetDefaultDb()
		go func() {
			for { //monitor event on chain
				l := <-ch
				ev := blockchain.NewEventAddressRegistered(&l)
				log.Debug(fmt.Sprintf("receive register socket:%#v", ev))
				c.put(ev.EthAddress, ev.Socket)
			}

		}()
	}
	return c
}

func (this *ContractDiscovery) put(addr common.Address, hostport string) {
	this.cacheAddr[addr] = hostport
	this.cacheSocket[hostport] = addr
	//this.db.Put(append(nodePrefix, addr[:]...), []byte(hostport))
	//this.db.Put(append(hostportPrefix, []byte(hostport)...), addr[:])
}
func (this *ContractDiscovery) Register(node common.Address, host string, port int) error {
	if node != this.node {
		log.Crit("register node to contract with unknown addr ", common.Bytes2Hex(node[:]))
	}
	hostport := tohostport(host, port)
	tx, err := this.discovery.RegisterEndpoint(this.auth, hostport)
	if err != nil {
		return err
	}
	//wait for completion ?
	_, err = bind.WaitMined(context.Background(), this.client, tx)
	if err != nil {
		return err
	}
	return nil
}

func (this *ContractDiscovery) Get(node common.Address) (host string, port int, err error) {
	//hostport, err := this.db.Get(append(nodePrefix, node[:]...))
	hostport, ok := this.cacheAddr[node]
	if ok {
		host, port = SplitHostPort(string(hostport))
		return
	}
	hostportstr, err := this.discovery.FindEndpointByAddress(nil, node)
	if err == nil && len(hostportstr) > 0 {
		this.put(node, hostportstr)
		host, port = SplitHostPort(hostportstr)
	}
	return
}

func (this *ContractDiscovery) NodeIdByHostPort(host string, port int) (node common.Address, err error) {
	hostport := tohostport(host, port)
	//addr, err := this.db.Get(append(hostportPrefix, []byte(hostport)...))
	addr, ok := this.cacheSocket[hostport]
	if ok {
		return addr, nil
	}
	node, err = this.discovery.FindAddressByEndpoint(nil, hostport)
	if err != nil && node != utils.EmptyAddress {
		this.put(node, hostport)
	}
	return
}
