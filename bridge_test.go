package mup

import (
	//"labix.org/v2/mgo/bson"
	. "gopkg.in/check.v1"
	"time"
)

type BridgeSuite struct {
	LineServerSuite
	MgoSuite
	Bridge *Bridge
	Config *BridgeConfig
}

var _ = Suite(&BridgeSuite{})

func (s *BridgeSuite) SetUpSuite(c *C) {
	s.LineServerSuite.SetUpSuite(c)
	s.MgoSuite.SetUpSuite(c)
}

func (s *BridgeSuite) TearDownSuite(c *C) {
	s.LineServerSuite.TearDownSuite(c)
	s.MgoSuite.TearDownSuite(c)
}

func (s *BridgeSuite) SetUpTest(c *C) {
	s.LineServerSuite.SetUpTest(c)
	s.MgoSuite.SetUpTest(c)

	SetDebug(true)
	SetLogger(c)

	s.Config = &BridgeConfig{
		Database:    "localhost:50017/mup",
		AutoRefresh: -1, // Disable for testing.
	}

	err := s.Session.DB("").C("servers").Insert(M{
		"name":     "testserver",
		"host":     s.Addr.String(),
		"password": "password",
	})
	c.Assert(err, IsNil)

	s.Bridge, err = StartBridge(s.Config)
	c.Assert(err, IsNil)

	lserver := s.LineServer(0)
	c.Assert(lserver.ReadLine(), Equals, "PASS password")
	c.Assert(lserver.ReadLine(), Equals, "NICK mup")
	c.Assert(lserver.ReadLine(), Equals, "USER mup 0 0 :Mup Pet")
}

func (s *BridgeSuite) TearDownTest(c *C) {
	if s.Bridge != nil {
		s.Bridge.Stop()
	}

	s.LineServerSuite.TearDownTest(c)
	s.MgoSuite.TearDownTest(c)
}

func (s *BridgeSuite) TestConnect(c *C) {
	// SetUpTest does it all.
}

func (s *BridgeSuite) TestNickInUse(c *C) {
	lserver := s.LineServer(0)
	lserver.SendLine(":n.net 433 * mup :Nickname is already in use.")
	c.Assert(lserver.ReadLine(), Equals, "NICK mup_")
	lserver.SendLine(":n.net 433 * mup_ :Nickname is already in use.")
	c.Assert(lserver.ReadLine(), Equals, "NICK mup__")
}

func (s *BridgeSuite) TestPingPong(c *C) {
	lserver := s.LineServer(0)
	lserver.SendLine("PING :foo")
	c.Assert(lserver.ReadLine(), Equals, "PONG :foo")
}

func (s *BridgeSuite) TestPingPongPostAuth(c *C) {
	lserver := s.LineServer(0)
	lserver.SendLine(":n.net 001 mup :Welcome!")
	lserver.SendLine("PING :foo")
	c.Assert(lserver.ReadLine(), Equals, "PONG :foo")
}

func (s *BridgeSuite) TestQuit(c *C) {
	s.Bridge.Stop()
	c.Assert(s.LineServer(0).ReadLine(), Equals, "QUIT :brb")
}

func (s *BridgeSuite) TestQuitPostAuth(c *C) {
	lserver := s.LineServer(0)
	lserver.SendLine(":n.net 001 mup :Welcome!")
	s.Bridge.Stop()
	c.Assert(lserver.ReadLine(), Equals, "QUIT :brb")
}

func (s *BridgeSuite) TestJoinChannel(c *C) {
	lserver := s.LineServer(0)
	lserver.SendLine(":n.net 001 mup :Welcome!")

	servers := s.Session.DB("").C("servers")
	err := servers.Update(
		M{"name": "testserver"},
		M{"$set": M{"channels": []M{{"name": "#c1"}, {"name": "#c2"}}}},
	)
	c.Assert(err, IsNil)

	s.Bridge.Refresh()
	c.Assert(lserver.ReadLine(), Equals, "JOIN #c1,#c2")

	// Confirm it joined both channels.
	lserver.SendLine(":mup!~mup@10.0.0.1 JOIN #c1")
	lserver.SendLine(":mup!~mup@10.0.0.1 JOIN #c2")

	err = servers.Update(
		M{"name": "testserver"},
		M{"$set": M{"channels": []M{{"name": "#c1"}, {"name": "#c3"}}}},
	)
	c.Assert(err, IsNil)

	s.Bridge.Refresh()
	c.Assert(lserver.ReadLine(), Equals, "JOIN #c3")
	c.Assert(lserver.ReadLine(), Equals, "PART #c2")

	// Do not confirm, forcing it to retry.
	s.Bridge.Refresh()
	c.Assert(lserver.ReadLine(), Equals, "JOIN #c3")
	c.Assert(lserver.ReadLine(), Equals, "PART #c2")
}

func waitFor(condition func() bool) {
	now := time.Now()
	end := now.Add(1 * time.Second)
	for now.Before(end) {
		if condition() {
			return
		}
		time.Sleep(50 * time.Millisecond)
		now = time.Now()
	}
}

func (s *BridgeSuite) TestIncoming(c *C) {
	lserver := s.LineServer(0)
	lserver.SendLine(":n.net 001 mup :Welcome!")
	lserver.SendLine(":somenick!~someuser@somehost PRIVMSG mup :Hello mup!")

	incoming := s.Session.DB("").C("incoming")

	waitFor(func() bool {
		count, err := incoming.Find(nil).Count()
		c.Assert(err, IsNil)
		return count > 0
	})

	var messages []*Message
	err := incoming.Find(nil).All(&messages)
	c.Assert(err, IsNil)

	c.Assert(messages, HasLen, 1)

	messages[0].Id = ""

	c.Assert(messages[0], DeepEquals, &Message{
		Server:  "testserver",
		Prefix:  "somenick!~someuser@somehost",
		Nick:    "somenick",
		User:    "~someuser",
		Host:    "somehost",
		Cmd:     "PRIVMSG",
		Params:  []string{"mup", ":Hello mup!"},
		Target:  "mup",
		Text:    "Hello mup!",
		Bang:    "!",
		MupNick: "mup",
		MupChat: true,
		MupText: "Hello mup!",
	})
}

func (s *BridgeSuite) TestOutgoing(c *C) {
	c.Assert(s.Bridge.Stop(), IsNil)

	servers := s.Session.DB("").C("servers")
	err := servers.Update(
		M{"name": "testserver"},
		M{"$set": M{"channels": []M{{"name": "#test"}}}},
	)
	c.Assert(err, IsNil)

	outgoing := s.Session.DB("").C("outgoing")
	err = outgoing.Insert(&Message{
		Server: "testserver",
		Target: "someone",
		Text:   "Hello there!",
	})
	c.Assert(err, IsNil)

	bridge, err := StartBridge(s.Config)
	c.Assert(err, IsNil)
	defer bridge.Stop()

	lserver := s.LineServer(1)
	c.Assert(lserver.ReadLine(), Equals, "PASS password")
	c.Assert(lserver.ReadLine(), Equals, "NICK mup")
	c.Assert(lserver.ReadLine(), Equals, "USER mup 0 0 :Mup Pet")

	lserver.SendLine(":n.net 001 mup :Welcome!")

	c.Assert(lserver.ReadLine(), Equals, "JOIN #test")
	c.Assert(lserver.ReadLine(), Equals, "PRIVMSG someone :Hello there!")

	err = outgoing.Insert(&Message{
		Server: "testserver",
		Target: "someone",
		Text:   "Hello again!",
	})
	c.Assert(err, IsNil)

	c.Assert(lserver.ReadLine(), Equals, "PRIVMSG someone :Hello again!")
}
