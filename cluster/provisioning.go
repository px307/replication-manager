// replication-manager - Replication Manager Monitoring and CLI for MariaDB and MySQL
// Authors: Guillaume Lefranc <guillaume@signal18.io>
//          Stephane Varoqui  <stephane@mariadb.com>
// This source code is licensed under the GNU General Public License, version 3.

package cluster

import (
	"bytes"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tanji/replication-manager/dbhelper"
)

func readPidFromFile(pidfile string) (string, error) {
	d, err := ioutil.ReadFile(pidfile)
	if err != nil {
		return "", err
	}
	return string(bytes.TrimSpace(d)), nil
}

func (cluster *Cluster) InitClusterSemiSync() error {

	//cluster.sme.SetFailoverState()
	// create service template and post
	if cluster.conf.Enterprise {
		cluster.OpenSVCProvision()
	} else {
		for k, server := range cluster.servers {
			cluster.LogPrintf("INFO", "Starting Server %s", cluster.cfgGroup+strconv.Itoa(k))
			if server.Conn.Ping() == nil {
				cluster.LogPrintf("INFO", "DB Server is not stop killing now %s", server.URL)
				if server.Id == "" {
					pidfile, _ := dbhelper.GetVariableByName(server.Conn, "PID_FILE")
					pid, _ := readPidFromFile(pidfile)
					pidint, _ := strconv.Atoi(pid)
					server.Process, _ = os.FindProcess(pidint)
				}

				cluster.KillMariaDB(server)
			}

			cluster.InitMariaDB(server, server.Id, "semisync.cnf")
		}
	}
	//	cluster.sme.RemoveFailoverState()

	return nil
}

func (cluster *Cluster) ShutdownClusterSemiSync() error {
	if cluster.testStopCluster == false {
		return nil
	}

	cluster.sme.SetFailoverState()

	for _, server := range cluster.servers {
		cluster.KillMariaDB(server)

	}
	/*server.delete(&cluster.slaves)
	server.delete(&cluster.servers)*/

	cluster.servers = nil
	cluster.slaves = nil
	cluster.master = nil
	cluster.sme.UnDiscovered()
	cluster.newServerList()
	cluster.sme.RemoveFailoverState()

	return nil
}

func (cluster *Cluster) RollingUpgrade() {
}

func (cluster *Cluster) InitMariaDB(server *ServerMonitor, name string, conf string) error {
	if server.Host != "127.0.0.1" {
		cluster.LogPrintf("INFO", "Starting remote DB server will be Replication Manager Enterprise feature")
	}
	server.Id = name
	server.Conf = conf
	path := cluster.conf.WorkingDir + "/" + name
	os.RemoveAll(path)
	mvCommand := exec.Command("cp", "-rp", cluster.conf.ShareDir+"/tests/data", path)
	mvCommand.Run()
	/*err := misc.CopyDir(cluster.conf.ShareDir+"/tests/data", path)
	if err != nil {
		cluster.LogPrintf("ERRROR : %s", err)
		return err
	}*/

	time.Sleep(time.Millisecond * 2000)
	err := cluster.StartMariaDB(server)
	time.Sleep(time.Millisecond * 2000)
	if err == nil {
		_, err := server.Conn.Exec("grant all on *.* to root@'%' identified by ''")
		if err != nil {
			cluster.LogPrintf("TEST", "GRANTS %s", err)
		}
	}

	return nil
}

func (cluster *Cluster) KillMariaDB(server *ServerMonitor) error {

	if cluster.conf.Enterprise {
		cluster.OpenSVCStopService(server)
	} else {

		if server.Host != "127.0.0.1" {
			cluster.LogPrintf("INFO", "Killing remote DB server will be Replication Manager Enterprise feature")
		}
		cluster.LogPrintf("TEST", "Killing MariaDB %s %d", server.Id, server.Process.Pid)
		//	server.Process.Kill()
		killCmd := exec.Command("kill", "-9", fmt.Sprintf("%d", server.Process.Pid))
		killCmd.Run()
		//cluster.waitMariaDBStop(server)
	}
	return nil
}

func (cluster *Cluster) ShutdownMariaDB(server *ServerMonitor) error {
	_, _ = server.Conn.Exec("SHUTDOWN")
	return nil
}

func (cluster *Cluster) StartMariaDB(server *ServerMonitor) error {

	if cluster.conf.Enterprise {
		cluster.OpenSVCStartService(server)
	} else {

		cluster.LogPrintf("TEST", "Starting MariaDB %s", server.Id)
		if server.Id == "" {

			_, err := os.Stat(server.Id)
			if err != nil {
				cluster.LogPrintf("TEST", "Starting MariaDB need bootstrap")
			}

		}
		path := cluster.conf.WorkingDir + "/" + server.Id
		err := os.RemoveAll(path + "/" + server.Id + ".pid")
		if err != nil {
			cluster.LogPrintf("ERROR", "%s", err)
			return err
		}
		usr, err := user.Current()
		if err != nil {
			cluster.LogPrintf("ERROR", "%s", err)
			return err
		}
		mariadbdCmd := exec.Command(cluster.conf.MariaDBBinaryPath+"/mysqld", "--defaults-file="+cluster.conf.ShareDir+"/tests/etc/"+server.Conf, "--port="+server.Port, "--server-id="+server.Port, "--datadir="+path, "--socket="+cluster.conf.WorkingDir+"/"+server.Id+".sock", "--user="+usr.Username, "--general_log=1", "--general_log_file="+path+"/"+server.Id+".log", "--pid_file="+path+"/"+server.Id+".pid", "--log-error="+path+"/"+server.Id+".err")
		cluster.LogPrintf("INFO", "%s %s", mariadbdCmd.Path, mariadbdCmd.Args)
		mariadbdCmd.Start()
		server.Process = mariadbdCmd.Process

		exitloop := 0
		for exitloop < 30 {
			time.Sleep(time.Millisecond * 2000)
			cluster.LogPrintf("INFO", "Waiting MariaDB startup ..")
			dsn := "root:@unix(" + cluster.conf.WorkingDir + "/" + server.Id + ".sock)/?timeout=1s"
			conn, err2 := sqlx.Open("mysql", dsn)
			if err2 == nil {
				conn.Exec("set sql_log_bin=0")
				grants := "grant all on *.* to '" + server.User + "'@'%%' identified by '" + server.Pass + "'"
				conn.Exec("grant all on *.* to '" + server.User + "'@'%' identified by '" + server.Pass + "'")
				cluster.LogPrintf("INFO", "%s", grants)
				grants2 := "grant all on *.* to '" + server.User + "'@'127.0.0.1' identified by '" + server.Pass + "'"
				conn.Exec(grants2)
				exitloop = 100
			}
			exitloop++

		}
		if exitloop == 101 {
			cluster.LogPrintf("INFO", "MariaDB started.")

		} else {
			cluster.LogPrintf("INFO", "MariaDB timeout.")
			return errors.New("Failed to start")
		}
	}
	return nil
}

func (cluster *Cluster) StartAllNodes() error {

	return nil
}

func (cluster *Cluster) WaitFailoverEndState() {
	for cluster.sme.IsInFailover() {
		time.Sleep(time.Second)
		cluster.LogPrintf("TEST", "Waiting for failover stopped.")
	}
	time.Sleep(recoverTime * time.Second)
}

func (cluster *Cluster) WaitFailoverEnd() error {
	cluster.WaitFailoverEndState()
	return nil

	// following code deadlock they may be cases where the channel blocked lacking a receiver
	/*exitloop := 0
	ticker := time.NewTicker(time.Millisecond * 2000)
	for exitloop < 30 {
		select {
		case <-ticker.C:
			cluster.LogPrint("TEST: Waiting Failover startup")
			exitloop++
		case sig := <-endfailoverChan:
			if sig {
				exitloop = 100
			}
		default:
		}
	}
	if exitloop == 100 {
		cluster.LogPrintf("TEST: Failover started")
	} else {
		cluster.LogPrintf("TEST: Failover timeout")
		return errors.New("Failed to Failover")
	}
	return nil*/
}

func (cluster *Cluster) WaitFailover(wg *sync.WaitGroup) {

	defer wg.Done()
	exitloop := 0
	ticker := time.NewTicker(time.Millisecond * 2000)
	for exitloop < 30 {
		select {
		case <-ticker.C:
			cluster.LogPrintf("TEST", "Waiting Failover end")
			exitloop++
		case <-cluster.failoverCond.Recv:
			return
		default:
		}
	}
	if exitloop == 100 {
		cluster.LogPrintf("TEST", "Failover end")
	} else {
		cluster.LogPrintf("TEST", "Failover end timeout")
		return
	}
	return
}

func (cluster *Cluster) WaitSwitchover(wg *sync.WaitGroup) {

	defer wg.Done()
	exitloop := 0
	ticker := time.NewTicker(time.Millisecond * 2000)
	for exitloop < 30 {
		select {
		case <-ticker.C:
			cluster.LogPrint("TEST", "Waiting Switchover end")
			exitloop++
		case <-cluster.switchoverCond.Recv:
			return
		default:
		}
	}
	if exitloop == 100 {
		cluster.LogPrintf("TEST", "Switchover end")
	} else {
		cluster.LogPrintf("TEST", "Switchover end timeout")
		return
	}
	return
}

func (cluster *Cluster) WaitRejoin(wg *sync.WaitGroup) {

	defer wg.Done()

	exitloop := 0

	ticker := time.NewTicker(time.Millisecond * 2000)
	for exitloop < 30 {

		select {
		case <-ticker.C:
			cluster.LogPrintf("TEST", "Waiting Rejoin")
			exitloop++
		case <-cluster.rejoinCond.Recv:
			return

		default:

		}

	}
	if exitloop < 30 {
		cluster.LogPrintf("TEST", "Rejoin Finished")

	} else {
		cluster.LogPrintf("TEST", "Rejoin timeout")
		return
	}
	return
}

func (cluster *Cluster) WaitMariaDBStop(server *ServerMonitor) error {
	exitloop := 0
	ticker := time.NewTicker(time.Millisecond * 2000)
	for exitloop < 30 {
		select {
		case <-ticker.C:
			cluster.LogPrint("TEST", "Waiting MariaDB shutdown")
			exitloop++
			_, err := os.FindProcess(server.Process.Pid)
			if err != nil {
				exitloop = 100
			}
		default:
		}
	}
	if exitloop == 100 {
		cluster.LogPrintf("TEST", "MariaDB shutdown")
	} else {
		cluster.LogPrintf("TEST", "MariaDB shutdown timeout")
		return errors.New("Failed to Stop MariaDB")
	}
	return nil
}

func (cluster *Cluster) WaitBootstrapDiscovery() error {
	cluster.LogPrint("TEST: Waiting Bootstrap and discovery")
	exitloop := 0
	ticker := time.NewTicker(time.Millisecond * 2000)
	for exitloop < 30 {
		select {
		case <-ticker.C:
			cluster.LogPrintf("TEST", "Waiting Bootstrap and discovery")
			exitloop++
			if cluster.sme.IsDiscovered() {
				exitloop = 100
			}
		default:
		}
	}
	if exitloop == 100 {
		cluster.LogPrintf("TEST", "Cluster is Bootstraped and discovery")
	} else {
		cluster.LogPrintf("TEST", "Bootstrap timeout")
		return errors.New("Failed Bootstrap timeout")
	}
	return nil
}

func (cluster *Cluster) waitMasterDiscovery() error {
	cluster.LogPrintf("TEST", "Waiting Master Found")
	exitloop := 0
	ticker := time.NewTicker(time.Millisecond * 2000)
	for exitloop < 30 {
		select {
		case <-ticker.C:
			cluster.LogPrintf("TEST", "Waiting Master Found")
			exitloop++
			if cluster.master != nil {
				exitloop = 100
			}
		default:
		}
	}
	if exitloop == 100 {
		cluster.LogPrintf("TEST", "Master founded")
	} else {
		cluster.LogPrintf("TEST", "Master found timeout")
		return errors.New("Failed Master search timeout")
	}
	return nil
}

// Bootstrap provisions && setup topology
func (cluster *Cluster) Bootstrap() error {
	var err error
	// create service template and post
	err = cluster.BootstrapServices()
	if err != nil {
		return err
	}
	time.Sleep(time.Millisecond * 3000)
	err = cluster.BootstrapReplication()
	if err != nil {
		return err
	}
	return nil
}

func (cluster *Cluster) BootstrapServices() error {

	// create service template and post
	if cluster.conf.Enterprise {
		err := cluster.InitClusterSemiSync()
		if err != nil {
			return err
		}
	}

	return nil
}

func (cluster *Cluster) BootstrapReplicationCleanup() error {

	cluster.LogPrintf("INFO", "Cleaning up replication on existing servers")
	cluster.sme.SetFailoverState()
	for _, server := range cluster.servers {
		if cluster.conf.Verbose {
			cluster.LogPrintf("INFO", "SetDefaultMasterConn on server %s ", server.URL)
		}
		err := dbhelper.SetDefaultMasterConn(server.Conn, cluster.conf.MasterConn)
		if err != nil {
			if cluster.conf.Verbose {
				cluster.LogPrintf("INFO", "RemoveFailoverState on server %s ", server.URL)
			}
			cluster.sme.RemoveFailoverState()
			return err
		}
		if cluster.conf.Verbose {
			cluster.LogPrintf("INFO", "ResetMaster on server %s ", server.URL)
		}
		err = dbhelper.ResetMaster(server.Conn)
		if err != nil {
			cluster.sme.RemoveFailoverState()
			return err
		}
		err = dbhelper.StopAllSlaves(server.Conn)
		if err != nil {
			cluster.sme.RemoveFailoverState()
			return err
		}
		err = dbhelper.ResetAllSlaves(server.Conn)
		if err != nil {
			cluster.sme.RemoveFailoverState()
			return err
		}
		_, err = server.Conn.Exec("SET GLOBAL gtid_slave_pos=''")
		if err != nil {
			cluster.sme.RemoveFailoverState()
			return err
		}
	}
	cluster.master = nil
	cluster.slaves = nil
	cluster.sme.RemoveFailoverState()
	return nil
}

func (cluster *Cluster) BootstrapReplication() error {

	// default to master slave
	if cluster.CleanAll {
		cluster.BootstrapReplicationCleanup()
	}
	err := cluster.TopologyDiscover()
	if err == nil {
		return errors.New("Environment already has an existing master/slave setup")
	}
	cluster.sme.SetFailoverState()
	masterKey := 0
	if cluster.conf.PrefMaster != "" {
		masterKey = func() int {
			for k, server := range cluster.servers {
				if server.URL == cluster.conf.PrefMaster {
					cluster.sme.RemoveFailoverState()
					return k
				}
			}
			cluster.sme.RemoveFailoverState()
			return -1
		}()
	}
	if masterKey == -1 {
		return errors.New("Preferred master could not be found in existing servers")
	}
	_, err = cluster.servers[masterKey].Conn.Exec("RESET MASTER")
	if err != nil {
		cluster.LogPrintf("INFO", "RESET MASTER failed on master")
	}
	// master-slave
	if cluster.conf.MultiMaster == false && cluster.conf.MxsBinlogOn == false && cluster.conf.MultiTierSlave == false && cluster.conf.ForceSlaveNoGtid == false {

		for key, server := range cluster.servers {
			if key == masterKey {
				dbhelper.FlushTables(server.Conn)
				dbhelper.SetReadOnly(server.Conn, false)
				continue
			} else {

				stmt := fmt.Sprintf("CHANGE MASTER '%s' TO master_host='%s', master_port=%s, master_user='%s', master_password='%s', master_use_gtid=current_pos, master_connect_retry=%d, master_heartbeat_period=%d", cluster.conf.MasterConn, cluster.servers[masterKey].Host, cluster.servers[masterKey].Port, cluster.rplUser, cluster.rplPass, cluster.conf.MasterConnectRetry, 1)
				_, err := server.Conn.Exec(stmt)
				if err != nil {
					cluster.sme.RemoveFailoverState()
					return errors.New(fmt.Sprintln(stmt, err))
				}
				_, err = server.Conn.Exec("START SLAVE '" + cluster.conf.MasterConn + "'")
				if err != nil {
					cluster.sme.RemoveFailoverState()
					return errors.New(fmt.Sprintln("Can't start slave: ", err))
				}
				dbhelper.SetReadOnly(server.Conn, true)
			}
		}
		cluster.LogPrintf("INFO", "Environment bootstrapped with %s as master", cluster.servers[masterKey].URL)
	}
	//Old style replication
	if cluster.conf.MultiMaster == false && cluster.conf.MxsBinlogOn == false && cluster.conf.MultiTierSlave == false && cluster.conf.ForceSlaveNoGtid == true {
		masterKey := 0
		for key, server := range cluster.servers {

			if key == masterKey {
				server.Refresh()
				dbhelper.FlushTables(server.Conn)
				dbhelper.SetReadOnly(server.Conn, false)
				continue
			} else {

				err = dbhelper.ChangeMaster(server.Conn, dbhelper.ChangeMasterOpt{
					Host:      cluster.servers[masterKey].Host,
					Port:      cluster.servers[masterKey].Port,
					User:      cluster.rplUser,
					Password:  cluster.rplPass,
					Retry:     strconv.Itoa(cluster.conf.ForceSlaveHeartbeatRetry),
					Heartbeat: strconv.Itoa(cluster.conf.ForceSlaveHeartbeatTime),
					Mode:      "POSITIONAL",
					Logfile:   cluster.servers[masterKey].MasterLogFile,
					Logpos:    cluster.servers[masterKey].MasterLogPos,
				})
				if err != nil {
					cluster.sme.RemoveFailoverState()
					return err
				}
				_, err = server.Conn.Exec("START SLAVE '" + cluster.conf.MasterConn + "'")
				if err != nil {
					cluster.sme.RemoveFailoverState()
					return errors.New(fmt.Sprintln("Can't start slave: ", err))
				}
				dbhelper.SetReadOnly(server.Conn, true)
			}
		}
		cluster.LogPrintf("INFO", "Environment bootstrapped with old replication style and %s as master", cluster.servers[masterKey].URL)
	}

	// Slave realy
	if cluster.conf.MultiTierSlave == true {
		masterKey = 0
		relaykey := 1
		for key, server := range cluster.servers {
			if key == masterKey {
				dbhelper.FlushTables(server.Conn)
				dbhelper.SetReadOnly(server.Conn, false)
				continue
			} else {
				dbhelper.StopAllSlaves(server.Conn)
				dbhelper.ResetAllSlaves(server.Conn)

				if relaykey == key {
					stmt := fmt.Sprintf("CHANGE MASTER '%s' TO master_host='%s', master_port=%s, master_user='%s', master_password='%s', master_use_gtid=current_pos, master_connect_retry=%d, master_heartbeat_period=%d", cluster.conf.MasterConn, cluster.servers[masterKey].Host, cluster.servers[masterKey].Port, cluster.rplUser, cluster.rplPass, cluster.conf.MasterConnectRetry, 1)
					_, err := server.Conn.Exec(stmt)
					if err != nil {
						cluster.sme.RemoveFailoverState()
						return errors.New(fmt.Sprintln(stmt, err))
					}
					_, err = server.Conn.Exec("START SLAVE '" + cluster.conf.MasterConn + "'")
					if err != nil {
						cluster.sme.RemoveFailoverState()
						return errors.New(fmt.Sprintln("Can't start slave: ", err))
					}
				} else {
					stmt := fmt.Sprintf("CHANGE MASTER '%s' TO master_host='%s', master_port=%s, master_user='%s', master_password='%s', master_use_gtid=current_pos, master_connect_retry=%d, master_heartbeat_period=%d", cluster.conf.MasterConn, cluster.servers[relaykey].Host, cluster.servers[relaykey].Port, cluster.rplUser, cluster.rplPass, cluster.conf.MasterConnectRetry, 1)
					_, err := server.Conn.Exec(stmt)
					if err != nil {
						cluster.sme.RemoveFailoverState()
						return errors.New(fmt.Sprintln(stmt, err))
					}
					_, err = server.Conn.Exec("START SLAVE '" + cluster.conf.MasterConn + "'")
					if err != nil {
						cluster.sme.RemoveFailoverState()
						return errors.New(fmt.Sprintln("Can't start slave: ", err))
					}

				}
				dbhelper.SetReadOnly(server.Conn, true)
			}
		}
		cluster.LogPrintf("INFO", "Environment bootstrapped with %s as master", cluster.servers[masterKey].URL)
	}
	if cluster.conf.MultiMaster == true {
		for key, server := range cluster.servers {
			if key == 0 {

				stmt := fmt.Sprintf("CHANGE MASTER '%s' TO master_host='%s', master_port=%s, master_user='%s', master_password='%s', master_use_gtid=current_pos, master_connect_retry=%d, master_heartbeat_period=%d", cluster.conf.MasterConn, cluster.servers[1].Host, cluster.servers[1].Port, cluster.rplUser, cluster.rplPass, cluster.conf.MasterConnectRetry, 1)
				_, err := server.Conn.Exec(stmt)
				if err != nil {
					cluster.sme.RemoveFailoverState()
					return errors.New(fmt.Sprintln(stmt, err))
				}
				_, err = server.Conn.Exec("START SLAVE '" + cluster.conf.MasterConn + "'")
				if err != nil {
					cluster.sme.RemoveFailoverState()
					return errors.New(fmt.Sprintln("Can't start slave: ", err))
				}
				dbhelper.SetReadOnly(server.Conn, true)
			}
			if key == 1 {

				stmt := fmt.Sprintf("CHANGE MASTER '%s' TO master_host='%s', master_port=%s, master_user='%s', master_password='%s', master_use_gtid=current_pos, master_connect_retry=%d, master_heartbeat_period=%d", cluster.conf.MasterConn, cluster.servers[0].Host, cluster.servers[0].Port, cluster.rplUser, cluster.rplPass, cluster.conf.MasterConnectRetry, 1)
				_, err := server.Conn.Exec(stmt)
				if err != nil {
					cluster.sme.RemoveFailoverState()
					return errors.New(fmt.Sprintln("ERROR:", stmt, err))
				}
				_, err = server.Conn.Exec("START SLAVE '" + cluster.conf.MasterConn + "'")
				if err != nil {
					cluster.sme.RemoveFailoverState()
					return errors.New(fmt.Sprintln("Can't start slave: ", err))
				}
			}
			dbhelper.SetReadOnly(server.Conn, true)
		}
	}

	cluster.sme.RemoveFailoverState()
	err = cluster.TopologyDiscover()
	if err != nil {
		return errors.New("Can't found topology after bootstrap")
	}
	//bootstrapChan <- true
	return nil
}
