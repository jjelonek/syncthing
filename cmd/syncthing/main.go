// Copyright (C) 2014 Jakob Borg and Contributors (see the CONTRIBUTORS file).
// All rights reserved. Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.
//
// Copyright (C) 2014 recoded

package main

import (
	// begin of the recoded code
	"aisserver"
	"common"
	"filemanager"
	"httpserver"
	"io/ioutil"

	"github.com/jjelonek/license"
	// end of the recoded code

	"crypto/sha1"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"strconv"
	"strings"
	"time"

	"code.google.com/p/go.crypto/bcrypt"
	"github.com/juju/ratelimit"
	"github.com/syncthing/discosrv"
	"github.com/syncthing/syncthing/config"
	"github.com/syncthing/syncthing/discover"
	"github.com/syncthing/syncthing/events"
	"github.com/syncthing/syncthing/files"
	"github.com/syncthing/syncthing/logger"
	"github.com/syncthing/syncthing/model"
	"github.com/syncthing/syncthing/protocol"
	"github.com/syncthing/syncthing/upgrade"
	"github.com/syncthing/syncthing/upnp"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

var (
	Version     = "unknown-dev"
	BuildEnv    = "default"
	BuildStamp  = "0"
	BuildDate   time.Time
	BuildHost   = "unknown"
	BuildUser   = "unknown"
	LongVersion string
	GoArchExtra string // "", "v5", "v6", "v7"
)

const (
	exitSuccess            = 0
	exitError              = 1
	exitNoUpgradeAvailable = 2
	exitRestarting         = 3
	exitUpgrading          = 4
)

var l = logger.DefaultLogger
var innerProcess = os.Getenv("STNORESTART") != ""

func init() {

	if Version != "unknown-dev" {
		// If not a generic dev build, version string should come from git describe
		exp := regexp.MustCompile(`^v\d+\.\d+\.\d+(-[a-z0-9]+)*(\+\d+-g[0-9a-f]+)?(-dirty)?$`)
		if !exp.MatchString(Version) {
			l.Fatalf("Invalid version string %q;\n\tdoes not match regexp %v", Version, exp)
		}
	}

	stamp, _ := strconv.Atoi(BuildStamp)
	BuildDate = time.Unix(int64(stamp), 0)

	date := BuildDate.UTC().Format("2006-01-02 15:04:05 MST")
	LongVersion = fmt.Sprintf("syncthing %s (%s %s-%s %s) %s@%s %s", Version, runtime.Version(), runtime.GOOS, runtime.GOARCH, BuildEnv, BuildUser, BuildHost, date)

	if os.Getenv("STTRACE") != "" {
		logFlags = log.Ltime | log.Ldate | log.Lmicroseconds | log.Lshortfile
	}

}

var (
	cfg            config.Configuration
	myID           protocol.NodeID
	confDir        string
	logFlags       int = log.Ldate | log.Ltime
	writeRateLimit *ratelimit.Bucket
	readRateLimit  *ratelimit.Bucket
	stop           = make(chan int)
	discoverer     *discover.Discoverer
	externalPort   int
	cert           tls.Certificate
	logPrefix      string
)

const (
	usage      = "syncthing [options]"
	extraUsage = `The value for the -logflags option is a sum of the following:

   1  Date
   2  Time
   4  Microsecond time
   8  Long filename
  16  Short filename

I.e. to prefix each log line with date and time, set -logflags=3 (1 + 2 from
above). The value 0 is used to disable all of the above. The default is to
show time only (2).

The following enviroment variables are interpreted by syncthing:

 STGUIADDRESS  Override GUI listen address set in config. Expects protocol type
               followed by hostname or an IP address, followed by a port, such
               as "https://127.0.0.1:8888".

 STGUIAUTH     Override GUI authentication credentials set in config. Expects
               a colon separated username and password, such as "admin:secret".

 STGUIAPIKEY   Override GUI API key set in config.

 STNORESTART   Do not attempt to restart when requested to, instead just exit.
               Set this variable when running under a service manager such as
               runit, launchd, etc.

 STTRACE       A comma separated string of facilities to trace. The valid
               facility strings:
               - "beacon"   (the beacon package)
               - "discover" (the discover package)
               - "events"   (the events package)
               - "files"    (the files package)
               - "net"      (the main package; connections & network messages)
               - "model"    (the model package)
               - "scanner"  (the scanner package)
               - "stats"    (the stats package)
               - "upnp"     (the upnp package)
               - "xdr"      (the xdr package)
               - "all"      (all of the above)

 STGUIASSETS   Directory to load GUI assets from. Overrides compiled in assets.

 STPROFILER    Set to a listen address such as "127.0.0.1:9090" to start the
               profiler with HTTP access.

 STCPUPROFILE  Write a CPU profile to cpu-$pid.pprof on exit.

 STHEAPPROFILE Write heap profiles to heap-$pid-$timestamp.pprof each time
               heap usage increases.

 STPERFSTATS   Write running performance statistics to perf-$pid.csv. Not
               supported on Windows.

 GOMAXPROCS    Set the maximum number of CPU cores to use. Defaults to all
               available CPU cores.`
)

func init() {
	rand.Seed(time.Now().UnixNano())

}

// Command line options
var (
	reset             bool
	showVersion       bool
	doUpgrade         bool
	doUpgradeCheck    bool
	noBrowser         bool
	generateDir       string
	guiAddress        string
	guiAuthentication string
	guiAPIKey         string
)

func removeOldDir(path, mmsi string) {
	vesselId, _ := strconv.Atoi(mmsi)
	mask := fmt.Sprintf("%s%09d", common.ClientNodePrefix, vesselId)
	dirList := filemanager.GetDirList(path, mask, false, false)
	if len(dirList) == 0 {
		os.RemoveAll(path)
	}
}

func verifyLicenseOnServer(licenseFile, licenseServer string, log *logger.Logger) {
	var (
		macAddress string
		interList  []net.Interface
		err        error
	)
	if interList, err = net.Interfaces(); err != nil {
		log.Fatalf("[start]", "Problem with net interfaces")
	}
	for _, inter := range interList {
		if inter.Name == "eth0" || inter.Name == "en0" {
			macAddress = inter.HardwareAddr.String()
		}
	}
	if macAddress == "" {
		log.Fatalf("[start]", "Problem with mac address")
	}
	httpURL := fmt.Sprintf("http://%s/license?key=%s", licenseServer, macAddress)
	// fmt.Printf("License server: %q\n", httpURL)
	for {
		resp, err := http.Get(httpURL)
		if err == nil {
			if resp.StatusCode == 200 {
				body, _ := ioutil.ReadAll(resp.Body)
				// if ok, _, _, err := license.CheckLicenseExpirationFromMemory(body, []byte(aisserver.PK)); ok {
				if ok, _, _, _ := license.CheckLicenseExpirationFromMemory(body, []byte(aisserver.PK)); ok {
					mode := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
					file, _ := os.OpenFile(licenseFile, mode, 0600)
					file.WriteString(string(body))
					file.Close()
					// fmt.Printf("License has been acquired from the server and is valid\n")
					common.RestartSync()
				}
				/*
					else {
						if err != nil {
							fmt.Printf("License has been acquired from the server but is corrupted.\n")
						} else {
							fmt.Printf("License has been acquired from the server but expired.")
						}
					}
				*/
				resp.Body.Close()
			} else {
				// fmt.Printf("License for key=%q is not available.", macAddress)
			}
		} else {
			// fmt.Printf("License server not available.")
		}
		// fmt.Printf(" The next trial in 15 sec.\n")
		time.Sleep(15 * time.Second)
	}
}

func main() {
	flag.StringVar(&confDir, "home", getDefaultConfDir(), "Set configuration directory")
	flag.BoolVar(&reset, "reset", false, "Prepare to resync from cluster")
	flag.BoolVar(&showVersion, "version", false, "Show version")
	flag.BoolVar(&doUpgrade, "upgrade", false, "Perform upgrade")
	flag.BoolVar(&doUpgradeCheck, "upgrade-check", false, "Check for available upgrade")
	flag.BoolVar(&noBrowser, "no-browser", false, "Do not start browser")
	flag.StringVar(&generateDir, "generate", "", "Generate key in specified dir")
	flag.StringVar(&guiAddress, "gui-address", "", "Override GUI address")
	flag.StringVar(&guiAuthentication, "gui-authentication", "", "Override GUI authentication. Expects 'username:password'")
	flag.StringVar(&guiAPIKey, "gui-apikey", "", "Override GUI API key")
	flag.IntVar(&logFlags, "logflags", logFlags, "Set log flags")
	flag.Usage = usageFor(flag.CommandLine, usage, extraUsage)

	// begin of the recoded code
	var (
		vesselMode bool
		srcDir     string
	)
	flag.BoolVar(&vesselMode, "vessel", false, "vessel flag")
	flag.Parse()
	cfg := common.ReadConfigFile()
	noBrowser = !cfg.StartGui
	if vesselMode {
		// aisserver.GenKey()
		aisserver.LicenseFile = cfg.Vessel.License.File
		var event chan byte
		srcDir = cfg.Vessel.Dir
		mmsi := cfg.Vessel.Mmsi
		licenseOK, _, expirationTime, err := license.CheckLicenseExpirationFromFile(aisserver.LicenseFile, aisserver.PK)
		if mmsi == "" || !licenseOK {
			logger.RecodedLogger = logger.CreateConsoleLogger()
			common.SetLogger(logger.RecodedLogger)
			go aisserver.StartWeb(cfg.Vessel.ConfigPort, "[start]", logger.RecodedLogger)
			if err != nil {
				logger.RecodedLogger.Warnf("[start]", "License file %q: %s", aisserver.LicenseFile, err.Error())
			} else {
				if !licenseOK {
					logger.RecodedLogger.Warnf("[start]", "License expirted at %s", expirationTime)
				}
			}
			var (
				macAddress string
				interList  []net.Interface
			)
			if interList, err = net.Interfaces(); err != nil {
				logger.RecodedLogger.Fatalf("[start]", "Problem with net interfaces")
			}
			for _, inter := range interList {
				if inter.Name == "eth0" || inter.Name == "en0" {
					macAddress = inter.HardwareAddr.String()
				}
			}
			if macAddress == "" {
				logger.RecodedLogger.Fatalf("[start]", "Problem with mac address")
			}
			logger.RecodedLogger.Infof("[start]", "MAC address: %s\n", macAddress)
			if !licenseOK {
				licenseServer := cfg.Vessel.License.Server
				verifyLicenseOnServer(aisserver.LicenseFile, licenseServer, logger.RecodedLogger)
			}
			<-event
		}
		removeOldDir(srcDir, mmsi)
		vesselId, _ := strconv.Atoi(mmsi)
		vesselDir := fmt.Sprintf("%s%09d", common.ClientNodePrefix, vesselId)
		logDir := cfg.Vessel.Dir + string(os.PathSeparator) + vesselDir + string(os.PathSeparator)
		logger.RecodedLogger = logger.CreateFileConsoleLogger(logDir)
		common.SetLogger(logger.RecodedLogger)
		go aisserver.StartAll(cfg, logger.RecodedLogger)
		if !cfg.Vessel.RemoteAIS.Active {
			<-event
		}
	} else {
		srcDir = cfg.Server.Dir
		logger.RecodedLogger = logger.CreateFileConsoleLogger(srcDir)
		discosrvDir := srcDir + "/discosrv/"
		go discosrv.Start(discosrvDir, logger.RecodedLogger)
		time.Sleep(time.Second)
		go httpserver.Start(cfg, logger.RecodedLogger)
	}
	recodedLog := logger.RecodedLogger.Get()
	logger.DefaultLogger.Set(recodedLog)
	common.SetLogger(logger.RecodedLogger)
	model.SetLogger(recodedLog)
	discover.SetLogger(recodedLog)
	srcDir, _ = filepath.Abs(srcDir)
	confDir = srcDir + common.SyncConfigDir
	logPrefix = "[.....] "
	time.Sleep(time.Second)
	// end of the recoded code

	if showVersion {
		fmt.Println(LongVersion)
		return
	}

	l.SetFlags(logFlags)

	if generateDir != "" {
		dir := expandTilde(generateDir)

		info, err := os.Stat(dir)
		l.FatalErr(logPrefix, err)
		if !info.IsDir() {
			l.Fatalln(logPrefix, dir, "is not a directory")
		}

		cert, err := loadCert(dir, "")
		if err == nil {
			l.Warnln(logPrefix, "Key exists; will not overwrite.")
			l.Infoln(logPrefix, "Node ID:", protocol.NewNodeID(cert.Certificate[0]))
			return
		}

		newCertificate(dir, "")
		cert, err = loadCert(dir, "")
		l.FatalErr(logPrefix, err)
		if err == nil {
			l.Infoln(logPrefix, "Node ID:", protocol.NewNodeID(cert.Certificate[0]))
		}
		return
	}

	if doUpgrade || doUpgradeCheck {
		rel, err := upgrade.LatestRelease(strings.Contains(Version, "-beta"))
		if err != nil {
			l.Fatalln("Upgrade:", err) // exits 1
		}

		if upgrade.CompareVersions(rel.Tag, Version) <= 0 {
			l.Infof(logPrefix, "No upgrade available (current %q >= latest %q).", Version, rel.Tag)
			os.Exit(exitNoUpgradeAvailable)
		}

		l.Infof(logPrefix, "Upgrade available (current %q < latest %q)", Version, rel.Tag)

		if doUpgrade {
			err = upgrade.UpgradeTo(rel, GoArchExtra)
			if err != nil {
				l.Fatalln(logPrefix, "Upgrade:", err) // exits 1
			}
			l.Okf(logPrefix, "Upgraded to %q", rel.Tag)
			return
		} else {
			return
		}
	}

	if reset {
		resetRepositories()
		return
	}

	confDir = expandTilde(confDir)

	if info, err := os.Stat(confDir); err == nil && !info.IsDir() {
		l.Fatalln(logPrefix, "Config directory", confDir, "is not a directory")
	}

	// recoded modification
	syncthingMain()
	/*
		if os.Getenv("STNORESTART") != "" {
			syncthingMain()
		} else {
			monitorMain()
		}
	*/
}

func syncthingMain() {
	var err error

	if len(os.Getenv("GOGC")) == 0 {
		debug.SetGCPercent(25)
	}

	if len(os.Getenv("GOMAXPROCS")) == 0 {
		runtime.GOMAXPROCS(runtime.NumCPU())
	}

	events.Default.Log(events.Starting, map[string]string{"home": confDir})

	if _, err = os.Stat(confDir); err != nil && confDir == getDefaultConfDir() {
		// We are supposed to use the default configuration directory. It
		// doesn't exist. In the past our default has been ~/.syncthing, so if
		// that directory exists we move it to the new default location and
		// continue. We don't much care if this fails at this point, we will
		// be checking that later.

		var oldDefault string
		if runtime.GOOS == "windows" {
			oldDefault = filepath.Join(os.Getenv("AppData"), "Syncthing")
		} else {
			oldDefault = expandTilde("~/.syncthing")
		}
		if _, err := os.Stat(oldDefault); err == nil {
			os.MkdirAll(filepath.Dir(confDir), 0700)
			if err := os.Rename(oldDefault, confDir); err == nil {
				l.Infoln(logPrefix, "Moved config dir", oldDefault, "to", confDir)
			}
		}
	}

	// Ensure that our home directory exists and that we have a certificate and key.

	ensureDir(confDir, 0770)
	cert, err = loadCert(confDir, "")
	if err != nil {
		newCertificate(confDir, "")
		cert, err = loadCert(confDir, "")
		l.FatalErr(logPrefix, err)
	}

	myID = protocol.NewNodeID(cert.Certificate[0])
	logPrefix = fmt.Sprintf("[%s] ", myID.String()[:5])
	logger.LogPrefix = logPrefix
	l.SetPrefix(logPrefix)
	model.SetPrefix(logPrefix)
	discover.SetPrefix(logPrefix)

	l.Infoln(logPrefix, LongVersion)
	l.Infoln(logPrefix, "My ID:", myID)

	// Prepare to be able to save configuration

	cfgFile := filepath.Join(confDir, "config.xml")

	var myName string

	// Load the configuration file, if it exists.
	// If it does not, create a template.

	cfg, err = config.Load(cfgFile, myID)
	if err == nil {
		myCfg := cfg.GetNodeConfiguration(myID)
		if myCfg == nil || myCfg.Name == "" {
			myName, _ = os.Hostname()
		} else {
			myName = myCfg.Name
		}
	} else {
		l.Infoln(logPrefix, "No config file; starting with empty defaults")
		myName, _ = os.Hostname()
		defaultRepo := filepath.Join(getHomeDir(), "Sync")

		cfg = config.New(cfgFile, myID)
		cfg.Repositories = []config.RepositoryConfiguration{
			{
				ID:              "default",
				Directory:       defaultRepo,
				RescanIntervalS: 60,
				Nodes:           []config.RepositoryNodeConfiguration{{NodeID: myID}},
			},
		}
		cfg.Nodes = []config.NodeConfiguration{
			{
				NodeID:    myID,
				Addresses: []string{"dynamic"},
				Name:      myName,
			},
		}

		port, err := getFreePort("127.0.0.1", 8080)
		l.FatalErr(logPrefix, err)
		cfg.GUI.Address = fmt.Sprintf("127.0.0.1:%d", port)

		port, err = getFreePort("0.0.0.0", 22000)
		l.FatalErr(logPrefix, err)
		cfg.Options.ListenAddress = []string{fmt.Sprintf("0.0.0.0:%d", port)}

		/*
			// recoded modification
			cfg.Options.StartBrowser = startGui
		*/

		cfg.Save()

		l.Infof(logPrefix, "Edit %s to taste or use the GUI\n", cfgFile)
	}

	if profiler := os.Getenv("STPROFILER"); len(profiler) > 0 {
		go func() {
			l.Debugln(logPrefix, "Starting profiler on", profiler)
			runtime.SetBlockProfileRate(1)
			err := http.ListenAndServe(profiler, nil)
			if err != nil {
				l.Fatalln(logPrefix, err)
			}
		}()
	}

	// The TLS configuration is used for both the listening socket and outgoing
	// connections.

	tlsCfg := &tls.Config{
		Certificates:           []tls.Certificate{cert},
		NextProtos:             []string{"bep/1.0"},
		ServerName:             myID.String(),
		ClientAuth:             tls.RequestClientCert,
		SessionTicketsDisabled: true,
		InsecureSkipVerify:     true,
		MinVersion:             tls.VersionTLS12,
	}

	// If the read or write rate should be limited, set up a rate limiter for it.
	// This will be used on connections created in the connect and listen routines.

	if cfg.Options.MaxSendKbps > 0 {
		writeRateLimit = ratelimit.NewBucketWithRate(float64(1000*cfg.Options.MaxSendKbps), int64(5*1000*cfg.Options.MaxSendKbps))
	}
	if cfg.Options.MaxRecvKbps > 0 {
		readRateLimit = ratelimit.NewBucketWithRate(float64(1000*cfg.Options.MaxRecvKbps), int64(5*1000*cfg.Options.MaxRecvKbps))
	}

	// If this is the first time the user runs v0.9, archive the old indexes and config.
	archiveLegacyConfig()

	db, err := leveldb.OpenFile(filepath.Join(confDir, "index"), &opt.Options{CachedOpenFiles: 100})
	if err != nil {
		l.Fatalln(logPrefix, "Cannot open database:", err, "- Is another copy of Syncthing already running?")
	}

	// Remove database entries for repos that no longer exist in the config
	repoMap := cfg.RepoMap()
	for _, repo := range files.ListRepos(db) {
		if _, ok := repoMap[repo]; !ok {
			l.Infof(logPrefix, "Cleaning data for dropped repo %q", repo)
			files.DropRepo(db, repo)
		}
	}

	m := model.NewModel(confDir, &cfg, myName, "syncthing", Version, db)

nextRepo:
	for i, repo := range cfg.Repositories {
		if repo.Invalid != "" {
			continue
		}

		repo.Directory = expandTilde(repo.Directory)

		fi, err := os.Stat(repo.Directory)
		if m.LocalVersion(repo.ID) > 0 {
			// Safety check. If the cached index contains files but the
			// repository doesn't exist, we have a problem. We would assume
			// that all files have been deleted which might not be the case,
			// so mark it as invalid instead.
			if err != nil || !fi.IsDir() {
				l.Warnf(logPrefix, "Stopping repository %q - directory missing, but has files in index", repo.ID)
				cfg.Repositories[i].Invalid = "repo directory missing"
				continue nextRepo
			}
		} else if os.IsNotExist(err) {
			// If we don't have ny files in the index, and the directory does
			// exist, try creating it.
			err = os.MkdirAll(repo.Directory, 0700)
		}

		if err != nil {
			// If there was another error or we could not create the
			// directory, the repository is invalid.
			l.Warnf(logPrefix, "Stopping repository %q - %v", err)
			cfg.Repositories[i].Invalid = err.Error()
			continue nextRepo
		}

		m.AddRepo(repo)
	}

	// GUI

	guiCfg := overrideGUIConfig(cfg.GUI, guiAddress, guiAuthentication, guiAPIKey)

	if guiCfg.Enabled && guiCfg.Address != "" {
		addr, err := net.ResolveTCPAddr("tcp", guiCfg.Address)
		if err != nil {
			l.Fatalf("Cannot start GUI on %q: %v", guiCfg.Address, err)
		} else {
			var hostOpen, hostShow string
			switch {
			case addr.IP == nil:
				hostOpen = "localhost"
				hostShow = "0.0.0.0"
			case addr.IP.IsUnspecified():
				hostOpen = "localhost"
				hostShow = addr.IP.String()
			default:
				hostOpen = addr.IP.String()
				hostShow = hostOpen
			}

			var proto = "http"
			if guiCfg.UseTLS {
				proto = "https"
			}

			l.Infof(logPrefix, "Starting web GUI on %s://%s/", proto, net.JoinHostPort(hostShow, strconv.Itoa(addr.Port)))
			err := startGUI(guiCfg, os.Getenv("STGUIASSETS"), m)
			if err != nil {
				l.Fatalln(logPrefix, "Cannot start GUI:", err)
			}
			// recoded
			l.Infof(logPrefix, "Web GUI started\n")
			common.GuiServerStarted <- true
			//
			if !noBrowser && cfg.Options.StartBrowser && len(os.Getenv("STRESTART")) == 0 {
				openURL(fmt.Sprintf("%s://%s:%d", proto, hostOpen, addr.Port))
			}
		}
	}

	// Clear out old indexes for other nodes. Otherwise we'll start up and
	// start needing a bunch of files which are nowhere to be found. This
	// needs to be changed when we correctly do persistent indexes.
	for _, repoCfg := range cfg.Repositories {
		if repoCfg.Invalid != "" {
			continue
		}
		for _, node := range repoCfg.NodeIDs() {
			if node == myID {
				continue
			}
			m.Index(node, repoCfg.ID, nil)
		}
	}

	// Walk the repository and update the local model before establishing any
	// connections to other nodes.

	m.CleanRepos()
	l.Infoln(logPrefix, "Performing initial repository scan")
	m.ScanRepos()

	// Remove all .idx* files that don't belong to an active repo.

	validIndexes := make(map[string]bool)
	for _, repo := range cfg.Repositories {
		dir := expandTilde(repo.Directory)
		id := fmt.Sprintf("%x", sha1.Sum([]byte(dir)))
		validIndexes[id] = true
	}

	allIndexes, err := filepath.Glob(filepath.Join(confDir, "*.idx*"))
	if err == nil {
		for _, idx := range allIndexes {
			bn := filepath.Base(idx)
			fs := strings.Split(bn, ".")
			if len(fs) > 1 {
				if _, ok := validIndexes[fs[0]]; !ok {
					l.Infoln(logPrefix, "Removing old index", bn)
					os.Remove(idx)
				}
			}
		}
	}

	// The default port we announce, possibly modified by setupUPnP next.

	addr, err := net.ResolveTCPAddr("tcp", cfg.Options.ListenAddress[0])
	if err != nil {
		l.Fatalln(logPrefix, "Bad listen address:", err)
	}
	externalPort = addr.Port

	// UPnP

	if cfg.Options.UPnPEnabled {
		setupUPnP()
	}

	// Routine to connect out to configured nodes
	discoverer = discovery(externalPort)
	go listenConnect(myID, m, tlsCfg)

	time.Sleep(10 * time.Second)
	for _, repo := range cfg.Repositories {
		if repo.Invalid != "" {
			continue
		}

		// Routine to pull blocks from other nodes to synchronize the local
		// repository. Does not run when we are in read only (publish only) mode.
		if repo.ReadOnly {
			l.Okf(logPrefix, "Ready to synchronize %s (read only; no external updates accepted)", repo.ID)
			m.StartRepoRO(repo.ID)
		} else {
			l.Okf(logPrefix, "Ready to synchronize %s (read-write)", repo.ID)
			m.StartRepoRW(repo.ID, cfg.Options.ParallelRequests)
		}
	}

	if cpuprof := os.Getenv("STCPUPROFILE"); len(cpuprof) > 0 {
		f, err := os.Create(fmt.Sprintf("cpu-%d.pprof", os.Getpid()))
		if err != nil {
			log.Fatal(err)
		}
		pprof.StartCPUProfile(f)
		defer pprof.StopCPUProfile()
	}

	for _, node := range cfg.Nodes {
		if len(node.Name) > 0 {
			l.Infof(logPrefix, "Node %s is %q at %v", node.NodeID, node.Name, node.Addresses)
		}
	}

	if cfg.Options.URAccepted > 0 && cfg.Options.URAccepted < usageReportVersion {
		l.Infoln(logPrefix, "Anonymous usage report has changed; revoking acceptance")
		cfg.Options.URAccepted = 0
	}
	if cfg.Options.URAccepted >= usageReportVersion {
		go usageReportingLoop(m)
		go func() {
			time.Sleep(10 * time.Minute)
			err := sendUsageReport(m)
			if err != nil {
				l.Infoln(logPrefix, "Usage report:", err)
			}
		}()
	}

	/*
		// recoded modification
			if cfg.Options.RestartOnWakeup {
				go standbyMonitor()
			}
	*/

	events.Default.Log(events.StartupComplete, nil)
	go generateEvents()

	code := <-stop

	l.Okln(logPrefix, "Exiting")
	os.Exit(code)
}

func generateEvents() {
	for {
		time.Sleep(300 * time.Second)
		events.Default.Log(events.Ping, nil)
	}
}

func setupUPnP() {
	if len(cfg.Options.ListenAddress) == 1 {
		_, portStr, err := net.SplitHostPort(cfg.Options.ListenAddress[0])
		if err != nil {
			l.Warnln(logPrefix, "Bad listen address:", err)
		} else {
			// Set up incoming port forwarding, if necessary and possible
			port, _ := strconv.Atoi(portStr)
			igd, err := upnp.Discover()
			if err == nil {
				externalPort = setupExternalPort(igd, port)
				if externalPort == 0 {
					l.Warnln(logPrefix, "Failed to create UPnP port mapping")
				} else {
					l.Infoln(logPrefix, "Created UPnP port mapping - external port", externalPort)
				}
			} else {
				l.Infof(logPrefix, "No UPnP gateway detected")
				if debugNet {
					l.Debugf(logPrefix, "UPnP: %v", err)
				}
			}
			if cfg.Options.UPnPRenewal > 0 {
				go renewUPnP(port)
			}
		}
	} else {
		l.Warnln(logPrefix, "Multiple listening addresses; not attempting UPnP port mapping")
	}
}

func setupExternalPort(igd *upnp.IGD, port int) int {
	// We seed the random number generator with the node ID to get a
	// repeatable sequence of random external ports.
	rnd := rand.NewSource(certSeed(cert.Certificate[0]))
	for i := 0; i < 10; i++ {
		r := 1024 + int(rnd.Int63()%(65535-1024))
		err := igd.AddPortMapping(upnp.TCP, r, port, "syncthing", cfg.Options.UPnPLease*60)
		if err == nil {
			return r
		}
	}
	return 0
}

func renewUPnP(port int) {
	for {
		time.Sleep(time.Duration(cfg.Options.UPnPRenewal) * time.Minute)

		igd, err := upnp.Discover()
		if err != nil {
			continue
		}

		// Just renew the same port that we already have
		if externalPort != 0 {
			err = igd.AddPortMapping(upnp.TCP, externalPort, port, "syncthing", cfg.Options.UPnPLease*60)
			if err == nil {
				// l.Infoln(logPrefix, "Renewed UPnP port mapping - external port", externalPort)
				continue
			}
		}

		// Something strange has happened. We didn't have an external port before?
		// Or perhaps the gateway has changed?
		// Retry the same port sequence from the beginning.
		r := setupExternalPort(igd, port)
		if r != 0 {
			externalPort = r
			l.Infoln(logPrefix, "Updated UPnP port mapping - external port", externalPort)
			discoverer.StopGlobal()
			discoverer.StartGlobal(cfg.Options.GlobalAnnServer, uint16(r))
			continue
		}
		l.Warnln(logPrefix, "Failed to update UPnP port mapping - external port", externalPort)
	}
}

func resetRepositories() {
	suffix := fmt.Sprintf(".syncthing-reset-%d", time.Now().UnixNano())
	for _, repo := range cfg.Repositories {
		if _, err := os.Stat(repo.Directory); err == nil {
			l.Infof(logPrefix, "Reset: Moving %s -> %s", repo.Directory, repo.Directory+suffix)
			os.Rename(repo.Directory, repo.Directory+suffix)
		}
	}

	idx := filepath.Join(confDir, "index")
	os.RemoveAll(idx)
}

func archiveLegacyConfig() {
	pat := filepath.Join(confDir, "*.idx.gz*")
	idxs, err := filepath.Glob(pat)
	if err == nil && len(idxs) > 0 {
		// There are legacy indexes. This is probably the first time we run as v0.9.
		backupDir := filepath.Join(confDir, "backup-of-v0.8")
		err = os.MkdirAll(backupDir, 0700)
		if err != nil {
			l.Warnln(logPrefix, "Cannot archive config/indexes:", err)
			return
		}

		for _, idx := range idxs {
			l.Infof(logPrefix, "Archiving %s", filepath.Base(idx))
			os.Rename(idx, filepath.Join(backupDir, filepath.Base(idx)))
		}

		src, err := os.Open(filepath.Join(confDir, "config.xml"))
		if err != nil {
			l.Warnf(logPrefix, "Cannot archive config:", err)
			return
		}
		defer src.Close()

		dst, err := os.Create(filepath.Join(backupDir, "config.xml"))
		if err != nil {
			l.Warnf(logPrefix, "Cannot archive config:", err)
			return
		}
		defer dst.Close()

		l.Infoln(logPrefix, "Archiving config.xml")
		io.Copy(dst, src)
	}
}

func restart() {
	l.Infoln(logPrefix, "Restarting")
	stop <- exitRestarting
}

func shutdown() {
	l.Infoln(logPrefix, "Shutting down")
	stop <- exitSuccess
}

func listenConnect(myID protocol.NodeID, m *model.Model, tlsCfg *tls.Config) {
	var conns = make(chan *tls.Conn)

	// Listen
	for _, addr := range cfg.Options.ListenAddress {
		go listenTLS(conns, addr, tlsCfg)
	}

	// Connect
	go dialTLS(m, conns, tlsCfg)

next:
	for conn := range conns {
		certs := conn.ConnectionState().PeerCertificates
		if cl := len(certs); cl != 1 {
			l.Infof(logPrefix, "Got peer certificate list of length %d != 1 from %s; protocol error", cl, conn.RemoteAddr())
			conn.Close()
			continue
		}
		remoteCert := certs[0]
		remoteID := protocol.NewNodeID(remoteCert.Raw)

		if remoteID == myID {
			l.Infof(logPrefix, "Connected to myself (%s) - should not happen", remoteID)
			conn.Close()
			continue
		}

		if m.ConnectedTo(remoteID) {
			l.Infof(logPrefix, "Connected to already connected node (%s)", remoteID)
			conn.Close()
			continue
		}

		for _, nodeCfg := range cfg.Nodes {
			if nodeCfg.NodeID == remoteID {
				// Verify the name on the certificate. By default we set it to
				// "syncthing" when generating, but the user may have replaced
				// the certificate and used another name.
				certName := nodeCfg.CertName
				if certName == "" {
					certName = "syncthing"
				}
				err := remoteCert.VerifyHostname(certName)
				if err != nil {
					// Incorrect certificate name is something the user most
					// likely wants to know about, since it's an advanced
					// config. Warn instead of Info.
					l.Warnf(logPrefix, "Bad certificate from %s (%v): %v", remoteID, conn.RemoteAddr(), err)
					conn.Close()
					continue next
				}

				// If rate limiting is set, we wrap the connection in a
				// limiter.
				var wr io.Writer = conn
				if writeRateLimit != nil {
					wr = &limitedWriter{conn, writeRateLimit}
				}

				var rd io.Reader = conn
				if readRateLimit != nil {
					rd = &limitedReader{conn, readRateLimit}
				}

				name := fmt.Sprintf("%s-%s", conn.LocalAddr(), conn.RemoteAddr())
				protoConn := protocol.NewConnection(remoteID, rd, wr, m, name, nodeCfg.Compression)

				l.Infof(logPrefix, "Established secure connection to %s at %s", remoteID, name)
				if debugNet {
					l.Debugf(logPrefix, "cipher suite %04X", conn.ConnectionState().CipherSuite)
				}
				events.Default.Log(events.NodeConnected, map[string]string{
					"id":   remoteID.String(),
					"addr": conn.RemoteAddr().String(),
				})

				m.AddConnection(conn, protoConn)
				continue next
			}
		}

		events.Default.Log(events.NodeRejected, map[string]string{
			"node":    remoteID.String(),
			"address": conn.RemoteAddr().String(),
		})
		l.Infof(logPrefix, "Connection from %s with unknown node ID %s; ignoring", conn.RemoteAddr(), remoteID)
		conn.Close()
	}
}

func listenTLS(conns chan *tls.Conn, addr string, tlsCfg *tls.Config) {
	if debugNet {
		l.Debugln(logPrefix, "listening on", addr)
	}

	tcaddr, err := net.ResolveTCPAddr("tcp", addr)
	l.FatalErr(logPrefix, err)
	listener, err := net.ListenTCP("tcp", tcaddr)
	l.FatalErr(logPrefix, err)

	for {
		conn, err := listener.Accept()
		if err != nil {
			l.Warnln(logPrefix, "Accepting connection:", err)
			continue
		}

		if debugNet {
			l.Debugln(logPrefix, "connect from", conn.RemoteAddr())
		}

		tcpConn := conn.(*net.TCPConn)
		setTCPOptions(tcpConn)

		tc := tls.Server(conn, tlsCfg)
		err = tc.Handshake()
		if err != nil {
			l.Infoln(logPrefix, "TLS handshake:", err)
			tc.Close()
			continue
		}

		conns <- tc
	}

}

func dialTLS(m *model.Model, conns chan *tls.Conn, tlsCfg *tls.Config) {
	var delay time.Duration = 1 * time.Second
	for {
	nextNode:
		for _, nodeCfg := range cfg.Nodes {
			if nodeCfg.NodeID == myID {
				continue
			}

			if m.ConnectedTo(nodeCfg.NodeID) {
				continue
			}

			var addrs []string
			for _, addr := range nodeCfg.Addresses {
				if addr == "dynamic" {
					if discoverer != nil {
						t := discoverer.Lookup(nodeCfg.NodeID)
						if len(t) == 0 {
							continue
						}
						addrs = append(addrs, t...)
					}
				} else {
					addrs = append(addrs, addr)
				}
			}

			for _, addr := range addrs {
				host, port, err := net.SplitHostPort(addr)
				if err != nil && strings.HasPrefix(err.Error(), "missing port") {
					// addr is on the form "1.2.3.4"
					addr = net.JoinHostPort(addr, "22000")
				} else if err == nil && port == "" {
					// addr is on the form "1.2.3.4:"
					addr = net.JoinHostPort(host, "22000")
				}
				if debugNet {
					l.Debugln(logPrefix, "dial", nodeCfg.NodeID, addr)
				}

				raddr, err := net.ResolveTCPAddr("tcp", addr)
				if err != nil {
					if debugNet {
						l.Debugln(logPrefix, err)
					}
					continue
				}

				conn, err := net.DialTCP("tcp", nil, raddr)
				if err != nil {
					if debugNet {
						l.Debugln(logPrefix, err)
					}
					continue
				}

				setTCPOptions(conn)

				tc := tls.Client(conn, tlsCfg)
				err = tc.Handshake()
				if err != nil {
					l.Infoln(logPrefix, "TLS handshake:", err)
					tc.Close()
					continue
				}

				conns <- tc
				continue nextNode
			}
		}

		time.Sleep(delay)
		delay *= 2
		if maxD := time.Duration(cfg.Options.ReconnectIntervalS) * time.Second; delay > maxD {
			delay = maxD
		}
	}
}

func setTCPOptions(conn *net.TCPConn) {
	var err error
	if err = conn.SetLinger(0); err != nil {
		l.Infoln(logPrefix, err)
	}
	if err = conn.SetNoDelay(false); err != nil {
		l.Infoln(logPrefix, err)
	}
	if err = conn.SetKeepAlivePeriod(60 * time.Second); err != nil {
		l.Infoln(logPrefix, err)
	}
	if err = conn.SetKeepAlive(true); err != nil {
		l.Infoln(logPrefix, err)
	}
}

func discovery(extPort int) *discover.Discoverer {
	disc := discover.NewDiscoverer(myID, cfg.Options.ListenAddress)

	if cfg.Options.LocalAnnEnabled {
		l.Infoln(logPrefix, "Starting local discovery announcements")
		disc.StartLocal(cfg.Options.LocalAnnPort, cfg.Options.LocalAnnMCAddr)
	}

	if cfg.Options.GlobalAnnEnabled {
		l.Infoln(logPrefix, "Starting global discovery announcements")
		disc.StartGlobal(cfg.Options.GlobalAnnServer, uint16(extPort))
	}

	return disc
}

func ensureDir(dir string, mode int) {
	fi, err := os.Stat(dir)
	if os.IsNotExist(err) {
		err := os.MkdirAll(dir, 0700)
		l.FatalErr(logPrefix, err)
	} else if mode >= 0 && err == nil && int(fi.Mode()&0777) != mode {
		err := os.Chmod(dir, os.FileMode(mode))
		l.FatalErr(logPrefix, err)
	}
}

func getDefaultConfDir() string {
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(os.Getenv("LocalAppData"), "Syncthing")

	case "darwin":
		return expandTilde("~/Library/Application Support/Syncthing")

	default:
		if xdgCfg := os.Getenv("XDG_CONFIG_HOME"); xdgCfg != "" {
			return filepath.Join(xdgCfg, "syncthing")
		} else {
			return expandTilde("~/.config/syncthing")
		}
	}
}

func expandTilde(p string) string {
	if p == "~" {
		return getHomeDir()
	}

	p = filepath.FromSlash(p)
	if !strings.HasPrefix(p, fmt.Sprintf("~%c", os.PathSeparator)) {
		return p
	}

	return filepath.Join(getHomeDir(), p[2:])
}

func getHomeDir() string {
	var home string

	switch runtime.GOOS {
	case "windows":
		home = filepath.Join(os.Getenv("HomeDrive"), os.Getenv("HomePath"))
		if home == "" {
			home = os.Getenv("UserProfile")
		}
	default:
		home = os.Getenv("HOME")
	}

	if home == "" {
		l.Fatalln(logPrefix, "No home directory found - set $HOME (or the platform equivalent).")
	}

	return home
}

// getFreePort returns a free TCP port fort listening on. The ports given are
// tried in succession and the first to succeed is returned. If none succeed,
// a random high port is returned.
func getFreePort(host string, ports ...int) (int, error) {
	for _, port := range ports {
		c, err := net.Listen("tcp", fmt.Sprintf("%s:%d", host, port))
		if err == nil {
			c.Close()
			return port, nil
		}
	}

	c, err := net.Listen("tcp", host+":0")
	if err != nil {
		return 0, err
	}
	addr := c.Addr().(*net.TCPAddr)
	c.Close()
	return addr.Port, nil
}

func overrideGUIConfig(originalCfg config.GUIConfiguration, address, authentication, apikey string) config.GUIConfiguration {
	// Make a copy of the config
	cfg := originalCfg

	if address == "" {
		address = os.Getenv("STGUIADDRESS")
	}

	if address != "" {
		cfg.Enabled = true

		addressParts := strings.SplitN(address, "://", 2)
		switch addressParts[0] {
		case "http":
			cfg.UseTLS = false
		case "https":
			cfg.UseTLS = true
		default:
			l.Fatalln(logPrefix, "Unidentified protocol", addressParts[0])
		}
		cfg.Address = addressParts[1]
	}

	if authentication == "" {
		authentication = os.Getenv("STGUIAUTH")
	}

	if authentication != "" {
		authenticationParts := strings.SplitN(authentication, ":", 2)

		hash, err := bcrypt.GenerateFromPassword([]byte(authenticationParts[1]), 0)
		if err != nil {
			l.Fatalln(logPrefix, "Invalid GUI password:", err)
		}

		cfg.User = authenticationParts[0]
		cfg.Password = string(hash)
	}

	if apikey == "" {
		apikey = os.Getenv("STGUIAPIKEY")
	}

	if apikey != "" {
		cfg.APIKey = apikey
	}
	return cfg
}

func standbyMonitor() {
	restartDelay := time.Duration(60 * time.Second)
	now := time.Now()
	for {
		time.Sleep(10 * time.Second)
		if time.Since(now) > 2*time.Minute {
			l.Infoln(logPrefix, "Paused state detected, possibly woke up from standby. Restarting in", restartDelay)

			// We most likely just woke from standby. If we restart
			// immediately chances are we won't have networking ready. Give
			// things a moment to stabilize.
			time.Sleep(restartDelay)

			restart()
			return
		}
		now = time.Now()
	}
}
