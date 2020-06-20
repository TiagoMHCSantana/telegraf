// +build windows

package win_services

import (
	"fmt"
	"os"

	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/filter"
	"github.com/influxdata/telegraf/plugins/inputs"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// ServiceErr type
type ServiceErr struct {
	Message string
	Service string
	Err     error
}

func (e *ServiceErr) Error() string {
	return fmt.Sprintf("%s: '%s': %v", e.Message, e.Service, e.Err)
}

// IsPermission checks whether an error is related to permission or not
func IsPermission(err error) bool {
	if err, ok := err.(*ServiceErr); ok {
		return os.IsPermission(err.Err)
	}
	return false
}

// WinService provides interface for svc.Service
type WinService interface {
	Close() error
	Config() (mgr.Config, error)
	Query() (svc.Status, error)
}

// ManagerProvider sets interface for acquiring manager instance, like mgr.Mgr
type ManagerProvider interface {
	Connect() (WinServiceManager, error)
}

// WinServiceManager provides interface for mgr.Mgr
type WinServiceManager interface {
	Disconnect() error
	OpenService(name string) (WinService, error)
	ListServices() ([]string, error)
}

// WinSvcMgr is wrapper for mgr.Mgr implementing WinServiceManager interface
type WinSvcMgr struct {
	realMgr *mgr.Mgr
}

// Disconnect ends up the connection with the service manager
func (m *WinSvcMgr) Disconnect() error {
	return m.realMgr.Disconnect()
}

// OpenService opens a specific service
func (m *WinSvcMgr) OpenService(name string) (WinService, error) {
	return m.realMgr.OpenService(name)
}

// ListServices lists the services installed
func (m *WinSvcMgr) ListServices() ([]string, error) {
	return m.realMgr.ListServices()
}

// MgProvider is an implementation of WinServiceManagerProvider interface returning WinSvcMgr
type MgProvider struct {
}

// Connect connects to the service manager
func (rmr *MgProvider) Connect() (WinServiceManager, error) {
	scmgr, err := mgr.Connect()
	if err != nil {
		return nil, err
	}
	return &WinSvcMgr{scmgr}, nil
}

const sampleConfig = `
  ## Names of the services to monitor. Leave empty to monitor all the available services on the host
  service_names = [
    "LanmanServer",
    "TermService",
  ]
`

const description = "Input plugin to report Windows services info."

//WinServices is an implementation if telegraf.Input interface, providing info about Windows Services
type WinServices struct {
	Log telegraf.Logger

	ServiceNames []string `toml:"service_names"`
	mgrProvider  ManagerProvider
	filter       filter.Filter
}

// ServiceInfo type
type ServiceInfo struct {
	ServiceName string
	DisplayName string
	State       int
	StartUpMode int
}

// Description returns the description of the plugin
func (m *WinServices) Description() string {
	return description
}

// SampleConfig returns an example of configuration file for the plugin
func (m *WinServices) SampleConfig() string {
	return sampleConfig
}

func (m *WinServices) initFilter() error {
	var err error
	if len(m.ServiceNames) == 0 {
		m.ServiceNames = append(m.ServiceNames, "*")
	}
	m.filter, err = filter.Compile(m.ServiceNames)

	return err
}

// Gather collects samples from the objects tracked by the plugin
func (m *WinServices) Gather(acc telegraf.Accumulator) error {
	if m.filter == nil {
		err := m.initFilter()
		if err != nil {
			return err
		}
	}

	scmgr, err := m.mgrProvider.Connect()
	if err != nil {
		return fmt.Errorf("Could not open service manager: %s", err)
	}
	defer scmgr.Disconnect()

	serviceNames, err := listServices(scmgr, m.filter)
	if err != nil {
		return err
	}

	for _, srvName := range serviceNames {
		service, err := collectServiceInfo(scmgr, srvName)
		if err != nil {
			if IsPermission(err) {
				m.Log.Debug(err.Error())
			} else {
				m.Log.Error(err.Error())
			}
			continue
		}

		tags := map[string]string{
			"service_name": service.ServiceName,
		}
		//display name could be empty, but still valid service
		if len(service.DisplayName) > 0 {
			tags["display_name"] = service.DisplayName
		}

		fields := map[string]interface{}{
			"state":        service.State,
			"startup_mode": service.StartUpMode,
		}
		acc.AddFields("win_services", fields, tags)
	}

	return nil
}

// listServices returns a list of services to gather.
func listServices(scmgr WinServiceManager, filter filter.Filter) ([]string, error) {
	names, err := scmgr.ListServices()
	if err != nil {
		return nil, fmt.Errorf("Could not list services: %s", err)
	}

	var services []string
	for _, svc := range names {
		if filter.Match(svc) {
			services = append(services, svc)
		}
	}

	return services, nil
}

// collectServiceInfo gathers info about a service.
func collectServiceInfo(scmgr WinServiceManager, serviceName string) (*ServiceInfo, error) {
	srv, err := scmgr.OpenService(serviceName)
	if err != nil {
		return nil, &ServiceErr{
			Message: "could not open service",
			Service: serviceName,
			Err:     err,
		}
	}
	defer srv.Close()

	srvStatus, err := srv.Query()
	if err != nil {
		return nil, &ServiceErr{
			Message: "could not query service",
			Service: serviceName,
			Err:     err,
		}
	}

	srvCfg, err := srv.Config()
	if err != nil {
		return nil, &ServiceErr{
			Message: "could not get config of service",
			Service: serviceName,
			Err:     err,
		}
	}

	serviceInfo := &ServiceInfo{
		ServiceName: serviceName,
		DisplayName: srvCfg.DisplayName,
		StartUpMode: int(srvCfg.StartType),
		State:       int(srvStatus.State),
	}
	return serviceInfo, nil
}

func init() {
	inputs.Add("win_services", func() telegraf.Input {
		return &WinServices{
			mgrProvider: &MgProvider{},
		}
	})
}
