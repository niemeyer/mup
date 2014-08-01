package launchpad_test

import (
	"fmt"
	"testing"

	. "gopkg.in/check.v1"
	"gopkg.in/mgo.v2/bson"
	"gopkg.in/mup.v0"
	_ "gopkg.in/mup.v0/plugins/launchpad"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func Test(t *testing.T) { TestingT(t) }

var _ = Suite(&S{})

type S struct{}

type lpTest struct {
	plugin   string
	target   string
	send     []string
	recv     []string
	config   bson.M
	targets  []bson.M
	bugsText [][]int
	bugsForm url.Values
}

var lpTests = []lpTest{
	{
		plugin: "lpbugdata",
		send:   []string{"bug foo"},
		recv:   []string{"PRIVMSG nick :Oops: cannot parse bug id from argument: foo"},
	}, {
		plugin: "lpbugdata",
		send:   []string{"bug #123"},
		recv:   []string{"PRIVMSG nick :Bug #123: Title of 123 <tag1> <tag2> <Some Project:New> <Other:Confirmed for joe> <https://launchpad.net/bugs/123>"},
	}, {
		plugin: "lpbugdata",
		send:   []string{"bug 111 +bug/222 bugs/333"},
		recv: []string{
			"PRIVMSG nick :Bug #111: Title of 111 <https://launchpad.net/bugs/111>",
			"PRIVMSG nick :Bug #222: Title of 222 <https://launchpad.net/bugs/222>",
			"PRIVMSG nick :Bug #333: Title of 333 <https://launchpad.net/bugs/333>",
		},
	}, {
		plugin:  "lpbugdata",
		config:  bson.M{"overhear": false},
		targets: []bson.M{{"account": ""}},
		target:  "#chan",
		send:    []string{"foo bug #111"},
		recv:    []string(nil),
	}, {
		plugin:  "lpbugdata",
		config:  bson.M{"overhear": true},
		targets: []bson.M{{"account": ""}},
		target:  "#chan",
		send:    []string{"foo bug #111"},
		recv:    []string{"PRIVMSG #chan :Bug #111: Title of 111 <https://launchpad.net/bugs/111>"},
	}, {
		plugin:  "lpbugdata",
		targets: []bson.M{
			{"account": "", "config": bson.M{"overhear": true}},
		},
		target:  "#chan",
		send:    []string{"foo bug #111"},
		recv:    []string{"PRIVMSG #chan :Bug #111: Title of 111 <https://launchpad.net/bugs/111>"},
	}, {
		plugin:  "lpbugdata",
		targets: []bson.M{
			{"channel": "#chan", "config": bson.M{"overhear": false}},
			{"account": "", "config": bson.M{"overhear": true}},
		},
		target:  "#chan",
		send:    []string{"foo bug #111"},
		recv:    []string(nil),
	}, {
		plugin: "lpbugwatch",
		config: bson.M{
			"project":   "some-project",
			"polldelay": "50ms",
			"prefixnew": "Bug #%d is new",
			"prefixold": "Bug #%d is old",
			"options":   "foo=bar",
		},
		targets: []bson.M{
			{"account": "test", "channel": "#chan"},
		},
		bugsText: [][]int{{111, 333, 444, 555}, {111, 222, 444, 666}},
		bugsForm: url.Values{
			"foo": {"bar"},
		},
		recv: []string{
			"PRIVMSG #chan :Bug #222 is new: Title of 222 <https://launchpad.net/bugs/222>",
			"PRIVMSG #chan :Bug #333 is old: Title of 333 <https://launchpad.net/bugs/333>",
			"PRIVMSG #chan :Bug #555 is old: Title of 555 <https://launchpad.net/bugs/555>",
			"PRIVMSG #chan :Bug #666 is new: Title of 666 <https://launchpad.net/bugs/666>",
		},
	}, {
		plugin: "lpmergewatch",
		config: bson.M{
			"project":   "some-project",
			"polldelay": "50ms",
		},
		targets: []bson.M{
			{"account": "test", "channel": "#chan"},
		},
		recv: []string{
			"PRIVMSG #chan :Merge proposal changed [needs review]: Branch description. <https://launchpad.net/~user/+merge/111>",
			"PRIVMSG #chan :Merge proposal changed [merged]: Branch description. <https://launchpad.net/~user/+merge/333>",
			"PRIVMSG #chan :Merge proposal changed [approved]: Branch description. <https://launchpad.net/~user/+merge/111>",
			"PRIVMSG #chan :Merge proposal changed [rejected]: Branch description with a very long first line that never ends and continues (...) <https://launchpad.net/~user/+merge/444>",
		},
	},
}

func (s *S) SetUpTest(c *C) {
	mup.SetLogger(c)
	mup.SetDebug(true)
}

func (s *S) TearDownTest(c *C) {
	mup.SetLogger(nil)
	mup.SetDebug(false)
}

func (s *S) TestLaunchpad(c *C) {
	for i, test := range lpTests {
		c.Logf("Testing message #%d: %s", i, test.send)
		server := lpServer{
			bugsText: test.bugsText,
		}
		server.Start()
		if test.config == nil {
			test.config = bson.M{}
		}
		test.config["endpoint"] = server.URL()
		tester := mup.NewPluginTester(test.plugin)
		tester.SetConfig(test.config)
		tester.SetTargets(test.targets)
		tester.Start()
		tester.SendAll(test.target, test.send)
		if test.config["polldelay"] != "" {
			time.Sleep(250 * time.Millisecond)
		}
		tester.Stop()
		server.Stop()
		c.Assert(tester.RecvAll(), DeepEquals, test.recv)

		if test.bugsForm != nil {
			c.Assert(server.bugsForm, DeepEquals, test.bugsForm)
		}
	}
}

type lpServer struct {
	server *httptest.Server

	bugForm url.Values

	bugsForm url.Values
	bugsText [][]int
	bugsResp int

	mergesResp int
}

func (s *lpServer) Start() {
	s.server = httptest.NewServer(s)
}

func (s *lpServer) Stop() {
	s.server.Close()
}

func (s *lpServer) URL() string {
	return s.server.URL
}

func (s *lpServer) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	req.ParseForm()
	switch {
	case strings.HasPrefix(req.URL.Path, "/bugs/"):
		s.serveBug(w, req)
	case strings.HasPrefix(req.URL.Path, "/some-project/+bugs-text"):
		s.serveBugsText(w, req)
	case strings.HasPrefix(req.URL.Path, "/some-project") && req.FormValue("ws.op") == "getMergeProposals":
		s.serveMerges(w, req)
	default:
		panic("got unexpected request for " + req.URL.Path + " in test lpServer")
	}
}

func (s *lpServer) serveBug(w http.ResponseWriter, req *http.Request) {
	tasks := false
	path := strings.TrimPrefix(req.URL.Path, "/bugs/")
	if s := strings.TrimSuffix(path, "/bug_tasks"); s != path {
		tasks = true
		path = s
	}
	s.bugForm = req.Form
	id, err := strconv.Atoi(path)
	if err != nil {
		panic("invalid bug URL: " + req.URL.Path)
	}
	var res string
	if tasks {
		res = fmt.Sprintf(`{"entries": [
			{"status": "New", "bug_target_display_name": "Some Project"},
			{"status": "Confirmed", "bug_target_display_name": "Other", "assignee_link": "foo/~joe"}
		]}`)
	} else if id == 123 {
		res = fmt.Sprintf(`{
			"title": "Title of %d",
			"tags": ["tag1", "tag2"],
			"bug_tasks_collection_link": "%s/bugs/%d/bug_tasks"
		}`, id, s.URL(), id)
	} else {
		res = fmt.Sprintf(`{"title": "Title of %d"}`, id)
	}
	w.Write([]byte(res))
}

func (s *lpServer) serveBugsText(w http.ResponseWriter, req *http.Request) {
	s.bugsForm = req.Form
	for _, bugId := range s.bugsText[s.bugsResp] {
		w.Write([]byte(strconv.Itoa(bugId)))
		w.Write([]byte{'\n'})
	}
	if s.bugsResp+1 < len(s.bugsText) {
		s.bugsResp++
	}
}

// Merge proposal changed [needs review]: %s <%s>

func (s *lpServer) serveMerges(w http.ResponseWriter, req *http.Request) {
	e := []string{
		`{"queue_status": "Needs Review", "self_link": "http://foo/~user/+merge/999", "description": "Ignored."}`,
		`{"queue_status": "Needs Review", "self_link": "http://foo/~user/+merge/111", "description": "Branch description."}`,
		`{"queue_status": "Approved", "self_link": "http://foo/~user/+merge/111", "description": "Branch description. Foo."}`,
		`{"queue_status": "Merged", "self_link": "http://foo/~user/+merge/333", "description": "Branch description.\nFoo."}`,
		`{"queue_status": "Rejected", "self_link": "http://foo/~user/+merge/444",
		  "description": "Branch description with a very long first line that never ends and continues until being broken up."}`,
	}
	var entries []string
	switch s.mergesResp {
	case 0:
		entries = []string{e[0]}
		s.mergesResp++
	case 1:
		entries = []string{e[1]}
		s.mergesResp++
	case 2:
		entries = []string{e[1], e[3]}
		s.mergesResp++
	case 3:
		entries = []string{e[2], e[3], e[4]}
	}
	w.Write([]byte(`{"entries": [` + strings.Join(entries, ",") + `]}`))
}
