// +build windows

package sqlserver

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows/registry"

	_ "github.com/denisenkom/go-mssqldb" // go-mssqldb initialization
	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/plugins/inputs"
)

// SQLServer struct
type SQLServer struct {
	Servers                     []string `toml:"servers"`
	QueryVersion                int      `toml:"query_version"`
	AzureDB                     bool     `toml:"azuredb"`
	IncludeQuery                []string `toml:"include_query"`
	ExcludeQuery                []string `toml:"exclude_query"`
	LocalInstancesAutoDiscovery bool     `toml:"local_instances_auto_discovery"`
	TagKeys                     []string `toml:"tag_keys"`
	includeQueries              map[string]struct{}
	excludeQueries              map[string]struct{}
	tags                        map[string]struct{}
	queries                     MapQuery
	isInitialized               bool
}

// Query struct
type Query struct {
	Script         string
	OrderedColumns []string
}

// MapQuery type
type MapQuery map[string]Query

const sampleConfig = `
## Specify instances to monitor with a list of connection strings.
## All connection parameters are optional.
## By default, the host is localhost, listening on default port, TCP 1433.
##   for Windows, the user is the currently running AD user (SSO).
##   See https://github.com/denisenkom/go-mssqldb for detailed connection
##   parameters, in particular, tls connections can be created like so:
##   "encrypt=true;certificate=<cert>;hostNameInCertificate=<SqlServer host fqdn>"
# servers = [
#  "Server=192.168.1.10;Port=1433;User Id=<user>;Password=<pw>;app name=telegraf;log=1;",
# ]

## If you want to connect to all local mssql instances using Windows Authentication,
## uncomment the following line and set it to true
# local_instances_auto_discovery = false

## Optional parameter, setting this to 2 will use a new version
## of the collection queries that break compatibility with the original
## dashboards.
## Version 2 - is compatible from SQL Server 2012 and later versions and also for SQL Azure DB
query_version = 2

## If you are using AzureDB, setting this to true will gather resource utilization metrics
# azuredb = false

## Possible queries
## Version 2:
## - PerformanceCounters
## - WaitStatsCategorized
## - DatabaseIO
## - ServerProperties
## - MemoryClerk
## - Schedulers
## - SqlRequests
## - VolumeSpace
## - Cpu
## - AlwaysOnHealth
## - CachedPlans
## - InstanceWaits
## - PageLifeExpectancy
## - LogUsage
## - DatabasesByInstance
## - DatabasesOnAG
## - JobRuns
## - DatabaseProperties
## - Backups
## - OrphanedUsers
## - UsersSysadmin
## - LockedUsers
## - PolicyChecked
## - DiskUsage
## Version 1:
## - PerformanceCounters
## - WaitStatsCategorized
## - CPUHistory
## - DatabaseIO
## - DatabaseSize
## - DatabaseStats
## - DatabaseProperties
## - MemoryClerk
## - VolumeSpace
## - PerformanceMetrics

## A list of queries to include. If not specified, all the above listed queries are used.
# include_query = []

## A list of queries to explicitly ignore.
exclude_query = [ 'Schedulers' , 'SqlRequests']
`

// SampleConfig return the sample configuration
func (s *SQLServer) SampleConfig() string {
	return sampleConfig
}

// Description return plugin description
func (s *SQLServer) Description() string {
	return "Read metrics from Microsoft SQL Server"
}

func (s *SQLServer) initIncludeQueries() {
	s.includeQueries = make(map[string]struct{})
	var empty struct{}

	for _, query := range s.IncludeQuery {
		s.includeQueries[query] = empty
	}
}

func (s *SQLServer) initExcludeQueries() {
	s.excludeQueries = make(map[string]struct{})
	var empty struct{}

	for _, query := range s.ExcludeQuery {
		s.excludeQueries[query] = empty
	}
}

func (s *SQLServer) initTags() {
	s.tags = make(map[string]struct{})
	var empty struct{}

	for _, query := range s.TagKeys {
		s.tags[query] = empty
	}
}

func (s *SQLServer) initQueries() {
	s.initIncludeQueries()
	s.initExcludeQueries()
	s.initTags()

	filter := func(queryMetricName string) bool {
		var shouldInclude = true

		if len(s.includeQueries) > 0 {
			_, shouldInclude = s.includeQueries[queryMetricName]
		}
		if len(s.excludeQueries) > 0 {
			_, ok := s.excludeQueries[queryMetricName]
			shouldInclude = !ok
		}

		return shouldInclude
	}

	s.queries = make(MapQuery)
	queries := s.queries
	// If this is an AzureDB instance, grab some extra metrics
	if s.AzureDB {
		if filter("AzureDBResourceStats") {
			queries["AzureDBResourceStats"] = Query{Script: sqlAzureDBResourceStats}
		}
		if filter("AzureDBResourceGovernance") {
			queries["AzureDBResourceGovernance"] = Query{Script: sqlAzureDBResourceGovernance}
		}
	}

	// Decide if we want to run version 1 or version 2 queries
	if s.QueryVersion == 2 {
		if filter("PerformanceCounters") {
			queries["PerformanceCounters"] = Query{Script: sqlPerformanceCountersV2}
		}
		if filter("WaitStatsCategorized") {
			queries["WaitStatsCategorized"] = Query{Script: sqlWaitStatsCategorizedV2}
		}
		if filter("DatabaseIO") {
			queries["DatabaseIO"] = Query{Script: sqlDatabaseIOV2}
		}
		if filter("ServerProperties") {
			queries["ServerProperties"] = Query{Script: sqlServerPropertiesV2}
		}
		if filter("MemoryClerk") {
			queries["MemoryClerk"] = Query{Script: sqlMemoryClerkV2}
		}
		if filter("Schedulers") {
			queries["Schedulers"] = Query{Script: sqlServerSchedulersV2}
		}
		if filter("SqlRequests") {
			queries["SqlRequests"] = Query{Script: sqlServerRequestsV2}
		}
		if filter("VolumeSpace") {
			queries["VolumeSpace"] = Query{Script: sqlServerVolumeSpaceV2}
		}
		if filter("Cpu") {
			queries["Cpu"] = Query{Script: sqlServerCpuV2}
		}
		if filter("AlwaysOnHealth") {
			queries["AlwaysOnHealth"] = Query{Script: alwaysOnHealthV2}
		}
		if filter("CachedPlans") {
			queries["CachedPlans"] = Query{Script: sqlCachedPlansV2}
		}
		if filter("InstanceWaits") {
			queries["InstanceWaits"] = Query{Script: sqlInstanceWaitsV2}
		}
		if filter("PageLifeExpectancy") {
			queries["PageLifeExpectancy"] = Query{Script: sqlPageLifeExpectancyV2}
		}
		if filter("LogUsage") {
			queries["LogUsage"] = Query{Script: sqlLogUsageV2}
		}
		if filter("DatabasesByInstance") {
			queries["DatabasesByInstance"] = Query{Script: sqlDatabasesByInstanceV2}
		}
		if filter("DatabasesOnAG") {
			queries["DatabasesOnAG"] = Query{Script: sqlDatabasesOnAGV2}
		}
		if filter("JobRuns") {
			queries["JobRuns"] = Query{Script: sqlJobRunsV2}
		}
		if filter("DatabaseProperties") {
			queries["DatabaseProperties"] = Query{Script: sqlDatabasePropertiesV2}
		}
		if filter("Backups") {
			queries["Backups"] = Query{Script: sqlBackupsV2}
		}
		if filter("OrphanedUsers") {
			queries["OrphanedUsers"] = Query{Script: sqlOrphanedUsersV2}
		}
		if filter("UsersSysadmin") {
			queries["UsersSysadmin"] = Query{Script: sqlUsersSysadminV2}
		}
		if filter("LockedUsers") {
			queries["LockedUsers"] = Query{Script: sqlLockedUsersV2}
		}
		if filter("PolicyChecked") {
			queries["PolicyChecked"] = Query{Script: sqlPolicyCheckedV2}
		}
		if filter("DiskUsage") {
			queries["DiskUsage"] = Query{Script: sqlDiskUsageV2}
		}
	} else {
		if filter("PerformanceCounters") {
			queries["PerformanceCounters"] = Query{Script: sqlPerformanceCounters}
		}
		if filter("WaitStatsCategorized") {
			queries["WaitStatsCategorized"] = Query{Script: sqlWaitStatsCategorized}
		}
		if filter("CPUHistory") {
			queries["CPUHistory"] = Query{Script: sqlCPUHistory}
		}
		if filter("DatabaseIO") {
			queries["DatabaseIO"] = Query{Script: sqlDatabaseIO}
		}
		if filter("DatabaseSize") {
			queries["DatabaseSize"] = Query{Script: sqlDatabaseSize}
		}
		if filter("DatabaseStats") {
			queries["DatabaseStats"] = Query{Script: sqlDatabaseStats}
		}
		if filter("DatabaseProperties") {
			queries["DatabaseProperties"] = Query{Script: sqlDatabaseProperties}
		}
		if filter("MemoryClerk") {
			queries["MemoryClerk"] = Query{Script: sqlMemoryClerk}
		}
		if filter("VolumeSpace") {
			queries["VolumeSpace"] = Query{Script: sqlVolumeSpace}
		}
		if filter("PerformanceMetrics") {
			queries["PerformanceMetrics"] = Query{Script: sqlPerformanceMetrics}
		}
	}

	// Set a flag so we know that queries have already been initialized
	s.isInitialized = true
}

func (s *SQLServer) includeConnStringsForLocalInstances() {
	if instances, err := getLocalInstances(); err == nil {
		if hostname, err := os.Hostname(); err == nil {
			for _, instance := range instances {
				if strings.ToUpper(instance) == "MSSQLSERVER" {
					s.Servers = append(s.Servers, fmt.Sprintf("Server=%s;Integrated Security=SSPI;", hostname))
				} else {
					s.Servers = append(s.Servers, fmt.Sprintf(`Server=%s\%s;Integrated Security=SSPI;`, hostname, instance))
				}
			}
		}
	}
}

func getLocalInstances() ([]string, error) {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Microsoft SQL Server`, registry.QUERY_VALUE)
	if err != nil {
		return nil, err
	}
	defer key.Close()

	instances, _, err := key.GetStringsValue("InstalledInstances")
	if err != nil {
		return nil, err
	}

	return instances, nil
}

// Gather collect data from SQL Server
func (s *SQLServer) Gather(acc telegraf.Accumulator) error {
	if !s.isInitialized {
		s.initQueries()
		if s.LocalInstancesAutoDiscovery {
			s.includeConnStringsForLocalInstances()
		}
	}

	var wg sync.WaitGroup

	for _, serv := range s.Servers {
		for _, query := range s.queries {
			wg.Add(1)
			go func(serv string, query Query) {
				defer wg.Done()
				acc.AddError(s.gatherServer(serv, query, acc))
			}(serv, query)
		}
	}

	wg.Wait()
	return nil
}

func (s *SQLServer) gatherServer(server string, query Query, acc telegraf.Accumulator) error {
	// deferred opening
	conn, err := sql.Open("mssql", server)
	if err != nil {
		return err
	}
	defer conn.Close()

	// execute query
	rows, err := conn.Query(query.Script)
	if err != nil {
		return err
	}
	defer rows.Close()

	// grab the column information from the result
	query.OrderedColumns, err = rows.Columns()
	if err != nil {
		return err
	}

	for rows.Next() {
		err = s.accRow(query, acc, rows)
		if err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *SQLServer) accRow(query Query, acc telegraf.Accumulator, row *sql.Rows) error {
	var columnVars []interface{}
	var fields = make(map[string]interface{})

	// store the column name with its *interface{}
	columnMap := make(map[string]*interface{})
	for _, column := range query.OrderedColumns {
		columnMap[column] = new(interface{})
		// populate the array of interface{} with the pointers in the right order
		columnVars = append(columnVars, columnMap[column])
	}

	// deconstruct array of variables and send to Scan
	err := row.Scan(columnVars...)
	if err != nil {
		return err
	}

	// measurement: identified by the header
	// tags: all other fields of type string
	isTag := func(header string) bool {
		_, ok := s.tags[header]
		return ok
	}
	tags := map[string]string{}
	var measurement string
	for header, val := range columnMap {
		if header == "measurement" {
			measurement = (*val).(string)
		} else if isTag(header) {
			tags[header] = (*val).(string)
		} else {
			fields[header] = (*val)
		}
	}

	// add fields to Accumulator
	acc.AddFields(measurement, fields, tags, time.Now())

	return nil
}

func init() {
	inputs.Add("sqlserver", func() telegraf.Input {
		return &SQLServer{}
	})
}

const sqlDiskUsageV2 string = `SET DEADLOCK_PRIORITY -10;
IF (OBJECT_ID('tempdb..#Datafile_Size') IS NOT NULL) DROP TABLE #Datafile_Size
SELECT
	'sqlserver_disk_usage' as [measurement],
	REPLACE(@@SERVERNAME,'\',':') as [sql_instance],
    B.database_id AS database_id,
    B.[name] AS [database],
    A.state_desc,
    A.[type_desc],
    A.[file_id],
    A.[name],
    A.physical_name,
    CAST(C.total_bytes / 1073741824.0 AS NUMERIC(18, 2)) AS disk_total_size_GB,
    CAST(C.available_bytes / 1073741824.0 AS NUMERIC(18, 2)) AS disk_free_size_GB,
    CAST(A.size / 128 / 1024.0 AS NUMERIC(18, 2)) AS size_GB,
    CAST(A.max_size / 128 / 1024.0 AS NUMERIC(18, 2)) AS max_size_GB,
    CAST(
        (CASE
        WHEN A.growth <= 0 THEN A.size / 128 / 1024.0
            WHEN A.max_size <= 0 THEN C.total_bytes / 1073741824.0
            WHEN A.max_size / 128 / 1024.0 > C.total_bytes / 1073741824.0 THEN C.total_bytes / 1073741824.0
            ELSE A.max_size / 128 / 1024.0
        END) AS NUMERIC(18, 2)) AS max_real_size_GB,
    CAST(NULL AS NUMERIC(18, 2)) AS free_space_GB,
    (CASE WHEN A.is_percent_growth = 1 THEN A.growth ELSE CAST(A.growth / 128 AS NUMERIC(18, 2)) END) AS growth_MB,
    A.is_percent_growth,
    (CASE WHEN A.growth <= 0 THEN 0 ELSE 1 END) AS is_autogrowth_enabled,
    CAST(NULL AS NUMERIC(18, 2)) AS percent_used,
    CAST(NULL AS INT) AS growth_times
INTO
    #Datafile_Size
FROM
    sys.master_files        A   WITH(NOLOCK)
    JOIN sys.databases      B   WITH(NOLOCK)    ON  A.database_id = B.database_id
    CROSS APPLY sys.dm_os_volume_stats(A.database_id, A.[file_id]) C
 
    
UPDATE A
SET
    A.free_space_GB = (
    (CASE
        WHEN max_size_GB <= 0 THEN A.disk_free_size_GB
        WHEN max_real_size_GB > disk_free_size_GB THEN A.disk_free_size_GB
        ELSE max_real_size_GB - size_GB
    END)),
    A.percent_used = (size_GB / (CASE WHEN max_real_size_GB > disk_total_size_GB THEN A.disk_total_size_GB ELSE max_real_size_GB END)) * 100
FROM
    #Datafile_Size A
    
 
UPDATE A
SET
    A.growth_times =
    (CASE
        WHEN A.growth_MB <= 0 THEN 0
        WHEN A.is_percent_growth = 0 THEN (A.max_real_size_GB - A.size_GB) / (A.growth_MB / 1024.0)
        ELSE -1
    END)
FROM
    #Datafile_Size A
 
 
SELECT *
FROM #Datafile_Size
`

const sqlPolicyCheckedV2 string = `SET DEADLOCK_PRIORITY -10;
SELECT
	'sqlserver_policy_checked' AS [measurement],
	@@SERVERNAME AS [server],
	[name] AS [login_name],
	is_policy_checked,
	LOGINPROPERTY(name, 'islocked') AS [is_locked]
FROM sys.sql_logins (NOLOCK)
WHERE [name] NOT LIKE '#%'
`

const sqlLockedUsersV2 string = `SET DEADLOCK_PRIORITY -10;
SELECT	'sqlserver_locked_users' as [measurement],
		@@servername as [server],
		loginname as [login_name],
		denylogin as [deny_login],
		hasaccess as [has_access],
		isntuser as [is_nt_user],
		ISNULL(LOGINPROPERTY(loginname,'islocked'),0) as [is_locked]
FROM sys.syslogins [L] WITH (NOLOCK)
JOIN sys.server_principals [P] WITH (NOLOCK)
	ON [L].sid = [P].[sid]
WHERE [P].[type] <> 'C'
`

const sqlUsersSysadminV2 string = `SET DEADLOCK_PRIORITY -10;
SELECT	'sqlserver_users_sysadmin' as [measurement],
		@@SERVERNAME as [server],
		sv.name as [login_name],
		type_desc,
		is_disabled,
		sl.isntgroup as [is_nt_group],
		ISNULL(IS_SRVROLEMEMBER('sysadmin',sv.name),0) as [is_sysadmin]
FROM sys.server_principals sv WITH (NOLOCK)
JOIN sys.syslogins sl WITH (NOLOCK)
	ON sv.sid=sl.sid
ORDER BY sv.name
`

const sqlOrphanedUsersV2 string = `SET DEADLOCK_PRIORITY -10;
declare @usuarios_orfaos table (ds_database varchar(256), ds_usuario varchar(256))
declare @query varchar(2000)
declare @versao varchar(200)
declare @resultado xml

set @query = '
select
  ''?'' as [database_name],
  [?].sys.database_principals.[name] as [user]
from
  [?].sys.database_principals with(nolock)  
  left join [?].sys.server_principals with(nolock)  
  on [?].sys.database_principals.sid = [?].sys.server_principals.sid  
where [?].sys.server_principals.sid is null  
  and [?].sys.database_principals.authentication_type_desc = ''instance'''

insert into @usuarios_orfaos (ds_database, ds_usuario)
exec master.dbo.sp_MSforeachdb @query

set @resultado = null

set @resultado = (
    select
        ds_database as 'usuario/@database',
        ds_usuario as 'usuario/@usuario'
    from
        @usuarios_orfaos
    order by
        1, 2
    for xml path(''), root('usuarios_orfaos'), type
)

select 'sqlserver_orphaned_users' as [measurement],
@@servername as [server],
ds_database as [database],
ds_usuario as [username],
1 as [value]
from @usuarios_orfaos
`

const sqlBackupsV2 string = `SET DEADLOCK_PRIORITY -10;
WITH query AS (
	SELECT
		X.[name] AS [database],
		(CASE T.[type]
			WHEN 'D' THEN 'Full Backup'
			WHEN 'I' THEN 'Differential Backup'
			WHEN 'L' THEN 'TLog Backup'
			WHEN 'F' THEN 'File or filegroup'
			WHEN 'G' THEN 'Differential file'
			WHEN 'P' THEN 'Partial'
			WHEN 'Q' THEN 'Differential Partial'
			ELSE 'Unknown'
		END) AS [backup_type],
		X.recovery_model_desc AS [recovery_model],
		ISNULL(B.backup_start_date, CAST('1970-01-01' AS DATETIME)) AS [backup_start_date],
		ISNULL(B.backup_finish_date, CAST('1970-01-01' AS DATETIME)) AS [backup_finish_date],
		ISNULL(DATEDIFF(SECOND,B.backup_start_date, B.backup_finish_date), 0) AS [total_time_taken_s],
		B.expiration_date,
		B.[user_name],
		B.machine_name,
		B.is_password_protected,
		X.collation_name,
		B.is_copy_only,
		ISNULL(CONVERT(NUMERIC(20, 2), B.backup_size / 1048576), 0.0) AS [backup_size_mb],
		A.logical_device_name,
		A.physical_device_name,
		B.[name] AS backupset_name,
		B.[description],
		B.has_backup_checksums,
		B.is_damaged,
		B.has_incomplete_metadata,
		sys.fn_hadr_backup_is_preferred_replica(X.[name]) AS [is_backup_preferred_replica],
		ROW_NUMBER() OVER (PARTITION BY X.[name], T.[type] ORDER BY backup_finish_date DESC) AS rn
	FROM
		sys.databases X WITH (NOLOCK)
		JOIN (
			SELECT [name], 'D' AS [type] FROM sys.databases WITH (NOLOCK)
			UNION
			SELECT [name], 'L' AS [type] FROM sys.databases WITH (NOLOCK)
		) AS T
			ON X.[name] = T.[name]
		LEFT JOIN msdb.dbo.backupset B WITH (NOLOCK)
			ON X.[name] = B.[database_name] AND T.[type] = B.[type]
			AND B.[type] IN ('D', 'L')
		LEFT JOIN msdb.dbo.backupmediafamily A WITH (NOLOCK) ON A.media_set_id = B.media_set_id
	WHERE
		X.[name] NOT IN ('tempdb', 'model', 'master', 'msdb', 'DBAASCONTROLE')
)
SELECT
	'sqlserver_backup' AS [measurement],
	REPLACE(@@SERVERNAME,'\',':') AS [sql_instance],
	[database],
	backup_type,
	recovery_model,
	DATEDIFF_BIG(SECOND, backup_start_date, GETDATE()) AS [seconds_since_backup_start],
	DATEDIFF_BIG(SECOND, backup_finish_date, GETDATE()) AS [seconds_since_backup_finish],
	total_time_taken_s,
	expiration_date,
	[user_name],
	machine_name,
	is_password_protected,
	collation_name,
	is_copy_only,
	backup_size_mb,
	logical_device_name,
	physical_device_name,
	backupset_name,
	description,
	has_backup_checksums,
	is_damaged,
	has_incomplete_metadata,
	is_backup_preferred_replica
FROM query
WHERE rn=1
`

const sqlDatabasePropertiesV2 string = `SET DEADLOCK_PRIORITY -10;
SELECT
	'sqlserver_database_properties' AS [measurement],
	REPLACE(@@SERVERNAME,'\',':') AS [sql_instance],
	[name] AS [database],
	[compatibility_level],
	is_auto_close_on,
	is_auto_create_stats_on,
	is_auto_create_stats_incremental_on,
	is_auto_update_stats_on,
	is_auto_update_stats_async_on,
	is_auto_shrink_on,
	state,
	state_desc,
	is_trustworthy_on,
	is_parameterization_forced,
	is_read_only
FROM
	sys.databases WITH (NOLOCK)
`

const sqlJobRunsV2 string = `SET DEADLOCK_PRIORITY -10;
WITH jobs AS (
	SELECT
		jobs.name,
		DATEDIFF_BIG(SS,
			DATEADD(SS,
				jobhistory.run_duration,
				msdb.dbo.agent_datetime(jobhistory.run_date,jobhistory.run_time)),
			GETDATE()
		) AS seconds_since_exec,
		ROW_NUMBER() OVER (PARTITION BY jobs.name ORDER BY msdb.dbo.agent_datetime(jobhistory.run_date,jobhistory.run_time) DESC) AS rownum,
		run_status,
		CASE
			WHEN run_status = 0
				THEN 'Failed'
			WHEN run_status = 1
				THEN 'Succeeded'
			WHEN run_status = 2
				THEN 'Retry'
			WHEN run_status = 3
				THEN 'Canceled'
			WHEN run_status = 4
				THEN 'In Progress'
			ELSE 'Unknown'
		END AS run_status_desc,
		freq_type AS frequency,
		CASE
			WHEN freq_type = 1
				THEN 'One time only'
			WHEN freq_type = 4
				THEN 'Daily'
			WHEN freq_type = 8
				THEN 'Weekly'
			WHEN freq_type = 16
				THEN 'Monthly'
			WHEN freq_type = 32
				THEN 'Monthly, relative to freq_interval'
			WHEN freq_type = 64
				THEN 'Runs when the SQL Server Agent service starts'
			WHEN freq_type = 128
				THEN 'Runs when the computer is idle'
			ELSE 'Unknown'
		END AS frequency_desc,
		jobs.enabled AS is_job_enabled,
		schedules.enabled AS is_schedule_enabled
FROM
	msdb.dbo.sysjobs AS jobs
	INNER JOIN msdb.dbo.sysjobschedules AS jobschedules
		ON jobs.job_id = jobschedules.job_id
	INNER JOIN msdb.dbo.sysschedules AS schedules
		ON jobschedules.schedule_id = schedules.schedule_id
	LEFT JOIN msdb.dbo.sysjobhistory AS jobhistory
		ON (jobs.job_id = jobhistory.job_id) )

SELECT
	'sqlserver_job_runs' AS [measurement],
	REPLACE(@@SERVERNAME,'\',':') AS [sql_instance],
	name AS [job_name],
	seconds_since_exec,
	run_status,
	run_status_desc,
	frequency,
	frequency_desc,
	is_job_enabled,
	is_schedule_enabled
FROM jobs
WHERE rownum = 1
`

const sqlPageLifeExpectancyV2 string = `SET DEADLOCK_PRIORITY -10;
SELECT
	'sqlserver_page_life_expectancy' AS [measurement]
	, REPLACE(@@SERVERNAME,'\',':') AS [sql_instance]
	,[cntr_value] AS [value]
FROM sys.dm_os_performance_counters WITH (NOLOCK)
WHERE [object_name] LIKE '%Manager%'
AND [counter_name] = 'Page life expectancy'
`

const sqlLogUsageV2 string = `SET DEADLOCK_PRIORITY -10;
DECLARE @result TABLE
(
 [database] VARCHAR(150) ,
 [log_size_mb] FLOAT ,
 [percent_log_used] FLOAT ,
 [status] VARCHAR(100)
)

INSERT INTO @result
EXEC ( 'DBCC sqlperf(LOGSPACE) WITH NO_INFOMSGS')

SELECT 'sqlserver_log_usage' AS [measurement],
		REPLACE(@@SERVERNAME,'\',':') AS [sql_instance],
		[database],
		[log_size_mb],
		ROUND([percent_log_used], 2) AS [percent_log_used]
FROM @result
WHERE [database] NOT IN ('master', 'model', 'msdb', 'DBAASCONTROLE', 'MonitorRM')
`

const sqlDatabasesByInstanceV2 string = `SET DEADLOCK_PRIORITY -10;
SELECT
	'sqlserver_databases_per_instance' AS [measurement],
	REPLACE(@@SERVERNAME,'\',':') AS [sql_instance],
	COUNT(*) [qty_databases]
FROM sys.databases WITH (NOLOCK)
WHERE [name] NOT IN ('master','model','msdb','tempdb','DBAASCONTROLE','MonitorRM')
`

const sqlDatabasesOnAGV2 string = `SET DEADLOCK_PRIORITY -10;
SELECT
	'sqlserver_databases_on_ag' AS [measurement],
	REPLACE(@@SERVERNAME,'\',':') AS [sql_instance],
	[name] AS [database],
	DATEDIFF_BIG(MS, create_date, GETDATE()) AS [time_elapsed_create_ms],
	(SELECT SERVERPROPERTY('IsHadrEnabled')) [is_hadr_enabled],
	(SELECT SERVERPROPERTY('IsClustered')) [is_clustered],
	CASE WHEN [replica_id] IS NULL THEN 0 ELSE 1 END AS [is_on_ag]
FROM
	sys.databases WITH (NOLOCK)
WHERE
	DB_NAME(database_id) not in ('master', 'msdb', 'tempdb', 'model', 'DBAASCONTROLE','MonitorRM')
`

const alwaysOnHealthV2 string = `SET DEADLOCK_PRIORITY -10;
SELECT 'sqlserver_alwayson_health' AS [measurement]
    , ag.name AS [avaliability_group]
    , REPLACE(ar.replica_server_name, '\', ':') AS [server]
    , CASE WHEN ars.is_local = 1 THEN 'Local' ELSE 'Remote' END AS [db_location]
    , ars.role_desc AS [replica_role]
    , ars.synchronization_health AS [health_state]
FROM sys.availability_groups ag WITH (NOLOCK)
   JOIN sys.availability_replicas ar WITH (NOLOCK)
       ON ag.group_id = ar.group_id
   JOIN sys.dm_hadr_availability_replica_states ars WITH (NOLOCK)
       ON ar.replica_id=ars.replica_id
OPTION (RECOMPILE, MAXDOP 1);
`

const sqlCachedPlansV2 string = `SET DEADLOCK_PRIORITY -10;
SELECT 'sqlserver_cached_plans' AS [measurement],
REPLACE(@@SERVERNAME,'\',':') AS [sql_instance],
COUNT(*) AS cached_plans
FROM sys.dm_exec_cached_plans WITH (NOLOCK)
`

const sqlInstanceWaitsV2 string = `
SET DEADLOCK_PRIORITY -10;
WITH [Waits] AS
    (SELECT
        [wait_type],
        [wait_time_ms] / 1000.0 AS [WaitS],
        ([wait_time_ms] - [signal_wait_time_ms]) / 1000.0 AS [ResourceS],
        [signal_wait_time_ms] / 1000.0 AS [SignalS],
        [waiting_tasks_count] AS [WaitCount],
       100.0 * [wait_time_ms] / SUM ([wait_time_ms]) OVER() AS [Percentage],
        ROW_NUMBER() OVER(ORDER BY [wait_time_ms] DESC) AS [RowNum]
    FROM sys.dm_os_wait_stats WITH (NOLOCK)
    WHERE [wait_type] NOT IN (
        N'BROKER_EVENTHANDLER', N'BROKER_RECEIVE_WAITFOR',
        N'BROKER_TASK_STOP', N'BROKER_TO_FLUSH',
        N'BROKER_TRANSMITTER', N'CHECKPOINT_QUEUE',
        N'CHKPT', N'CLR_AUTO_EVENT',
        N'CLR_MANUAL_EVENT', N'CLR_SEMAPHORE',
 
        -- Maybe uncomment these four if you have mirroring issues
        N'DBMIRROR_DBM_EVENT', N'DBMIRROR_EVENTS_QUEUE',
        N'DBMIRROR_WORKER_QUEUE', N'DBMIRRORING_CMD',
 
        N'DIRTY_PAGE_POLL', N'DISPATCHER_QUEUE_SEMAPHORE',
        N'EXECSYNC', N'FSAGENT',
        N'FT_IFTS_SCHEDULER_IDLE_WAIT', N'FT_IFTSHC_MUTEX',
 
        -- Maybe uncomment these six if you have AG issues
        N'HADR_CLUSAPI_CALL', N'HADR_FILESTREAM_IOMGR_IOCOMPLETION',
        N'HADR_LOGCAPTURE_WAIT', N'HADR_NOTIFICATION_DEQUEUE',
        N'HADR_TIMER_TASK', N'HADR_WORK_QUEUE',
 
        N'KSOURCE_WAKEUP', N'LAZYWRITER_SLEEP',
        N'LOGMGR_QUEUE', N'MEMORY_ALLOCATION_EXT',
        N'ONDEMAND_TASK_QUEUE',
        N'PREEMPTIVE_XE_GETTARGETSTATE',
        N'PWAIT_ALL_COMPONENTS_INITIALIZED',
        N'PWAIT_DIRECTLOGCONSUMER_GETNEXT',
        N'QDS_PERSIST_TASK_MAIN_LOOP_SLEEP', N'QDS_ASYNC_QUEUE',
        N'QDS_CLEANUP_STALE_QUERIES_TASK_MAIN_LOOP_SLEEP',
        N'QDS_SHUTDOWN_QUEUE', N'REDO_THREAD_PENDING_WORK',
        N'REQUEST_FOR_DEADLOCK_SEARCH', N'RESOURCE_QUEUE',
        N'SERVER_IDLE_CHECK', N'SLEEP_BPOOL_FLUSH',
        N'SLEEP_DBSTARTUP', N'SLEEP_DCOMSTARTUP',
        N'SLEEP_MASTERDBREADY', N'SLEEP_MASTERMDREADY',
        N'SLEEP_MASTERUPGRADED', N'SLEEP_MSDBSTARTUP',
        N'SLEEP_SYSTEMTASK', N'SLEEP_TASK',
        N'SLEEP_TEMPDBSTARTUP', N'SNI_HTTP_ACCEPT',
        N'SP_SERVER_DIAGNOSTICS_SLEEP', N'SQLTRACE_BUFFER_FLUSH',
        N'SQLTRACE_INCREMENTAL_FLUSH_SLEEP',
        N'SQLTRACE_WAIT_ENTRIES', N'WAIT_FOR_RESULTS',
        N'WAITFOR', N'WAITFOR_TASKSHUTDOWN',
        N'WAIT_XTP_RECOVERY',
        N'WAIT_XTP_HOST_WAIT', N'WAIT_XTP_OFFLINE_CKPT_NEW_LOG',
        N'WAIT_XTP_CKPT_CLOSE', N'XE_DISPATCHER_JOIN',
        N'XE_DISPATCHER_WAIT', N'XE_TIMER_EVENT')
    AND [waiting_tasks_count] > 0
    )
SELECT
	'sqlserver_instance_waits' As [measurement],
	REPLACE(@@SERVERNAME,'\',':') AS [sql_instance],
    MAX ([W1].[wait_type]) AS [wait_type],
    CAST (MAX ([W1].[WaitS]) AS DECIMAL (16,2)) AS [wait_s],
    CAST (MAX ([W1].[ResourceS]) AS DECIMAL (16,2)) AS [resource_s],
    CAST (MAX ([W1].[SignalS]) AS DECIMAL (16,2)) AS [signal_s],
    MAX ([W1].[WaitCount]) AS [wait_count],
    CAST (MAX ([W1].[Percentage]) AS DECIMAL (5,2)) AS [percentage]
FROM [Waits] AS [W1]
INNER JOIN [Waits] AS [W2]
    ON [W2].[RowNum] <= [W1].[RowNum]
GROUP BY [W1].[RowNum]
HAVING SUM ([W2].[Percentage]) - MAX( [W1].[Percentage] ) < 95; -- percentage threshold
`

// Queries - V2
// Thanks Bob Ward (http://aka.ms/bobwardms)
// and the folks at Stack Overflow (https://github.com/opserver/Opserver/blob/9c89c7e9936b58ad237b30e6f4cc6cd59c406889/Opserver.Core/Data/SQL/SQLInstance.Memory.cs)
// for putting most of the memory clerk definitions online!
/*
The SQL scripts use a series of IF and CASE statemens to choose the correct query based on edition and version of SQL Server, below the meaning of the numbers:
EngineEdition:
1 = Personal or Desktop Engine (Not available in SQL Server 2005 (9.x) and later versions.)
2 = Standard (This is returned for Standard, Web, and Business Intelligence.)
3 = Enterprise (This is returned for Evaluation, Developer, and Enterprise editions.)
4 = Express (This is returned for Express, Express with Tools, and Express with Advanced Services)
5 = SQL Database
6 = Microsoft Azure Synapse Analytics (formerly SQL Data Warehouse)
8 = Managed Instance

ProductVersion:
see https://sqlserverbuilds.blogspot.com/ for all the details about the version number of SQL Server
*/

const sqlMemoryClerkV2 = `
SET DEADLOCK_PRIORITY -10;
DECLARE
	 @SqlStatement AS nvarchar(max)
	,@MajorMinorVersion AS int = CAST(PARSENAME(CAST(SERVERPROPERTY('ProductVersion') as nvarchar),4) AS int)*100 + CAST(PARSENAME(CAST(SERVERPROPERTY('ProductVersion') as nvarchar),3) AS int)
	,@Columns AS nvarchar(max) = ''

IF @MajorMinorVersion >= 1100
	SET @Columns += N'mc.[pages_kb]';
ELSE
	SET @Columns += N'mc.[single_pages_kb] + mc.[multi_pages_kb]';

SET @SqlStatement = N'
SELECT
	 ''sqlserver_memory_clerks'' AS [measurement]
	,REPLACE(@@SERVERNAME, ''\'', '':'') AS [sql_instance]
	,DB_NAME() AS [database_name]
	,ISNULL(clerk_names.[name],mc.[type]) AS [clerk_type]
	,SUM(' + @Columns + N') AS [size_kb]
FROM sys.[dm_os_memory_clerks] AS mc WITH (NOLOCK)
LEFT OUTER JOIN ( VALUES
	 (''CACHESTORE_BROKERDSH'',''Service Broker Dialog Security Header Cache'')
	,(''CACHESTORE_BROKERKEK'',''Service Broker Key Exchange Key Cache'')
	,(''CACHESTORE_BROKERREADONLY'',''Service Broker (Read-Only)'')
	,(''CACHESTORE_BROKERRSB'',''Service Broker Null Remote Service Binding Cache'')
	,(''CACHESTORE_BROKERTBLACS'',''Broker dormant rowsets'')
	,(''CACHESTORE_BROKERTO'',''Service Broker Transmission Object Cache'')
	,(''CACHESTORE_BROKERUSERCERTLOOKUP'',''Service Broker user certificates lookup result cache'')
	,(''CACHESTORE_CLRPROC'',''CLR Procedure Cache'')
	,(''CACHESTORE_CLRUDTINFO'',''CLR UDT Info'')
	,(''CACHESTORE_COLUMNSTOREOBJECTPOOL'',''Column Store Object Pool'')
	,(''CACHESTORE_CONVPRI'',''Conversation Priority Cache'')
	,(''CACHESTORE_EVENTS'',''Event Notification Cache'')
	,(''CACHESTORE_FULLTEXTSTOPLIST'',''Full Text Stoplist Cache'')
	,(''CACHESTORE_NOTIF'',''Notification Store'')
	,(''CACHESTORE_OBJCP'',''Object Plans'')
	,(''CACHESTORE_PHDR'',''Bound Trees'')
	,(''CACHESTORE_SEARCHPROPERTYLIST'',''Search Property List Cache'')
	,(''CACHESTORE_SEHOBTCOLUMNATTRIBUTE'',''SE Shared Column Metadata Cache'')
	,(''CACHESTORE_SQLCP'',''SQL Plans'')
	,(''CACHESTORE_STACKFRAMES'',''SOS_StackFramesStore'')
	,(''CACHESTORE_SYSTEMROWSET'',''System Rowset Store'')
	,(''CACHESTORE_TEMPTABLES'',''Temporary Tables & Table Variables'')
	,(''CACHESTORE_VIEWDEFINITIONS'',''View Definition Cache'')
	,(''CACHESTORE_XML_SELECTIVE_DG'',''XML DB Cache (Selective)'')
	,(''CACHESTORE_XMLDBATTRIBUTE'',''XML DB Cache (Attribute)'')
	,(''CACHESTORE_XMLDBELEMENT'',''XML DB Cache (Element)'')
	,(''CACHESTORE_XMLDBTYPE'',''XML DB Cache (Type)'')
	,(''CACHESTORE_XPROC'',''Extended Stored Procedures'')
	,(''MEMORYCLERK_FILETABLE'',''Memory Clerk (File Table)'')
	,(''MEMORYCLERK_FSCHUNKER'',''Memory Clerk (FS Chunker)'')
	,(''MEMORYCLERK_FULLTEXT'',''Full Text'')
	,(''MEMORYCLERK_FULLTEXT_SHMEM'',''Full-text IG'')
	,(''MEMORYCLERK_HADR'',''HADR'')
	,(''MEMORYCLERK_HOST'',''Host'')
	,(''MEMORYCLERK_LANGSVC'',''Language Service'')
	,(''MEMORYCLERK_LWC'',''Light Weight Cache'')
	,(''MEMORYCLERK_QSRANGEPREFETCH'',''QS Range Prefetch'')
	,(''MEMORYCLERK_SERIALIZATION'',''Serialization'')
	,(''MEMORYCLERK_SNI'',''SNI'')
	,(''MEMORYCLERK_SOSMEMMANAGER'',''SOS Memory Manager'')
	,(''MEMORYCLERK_SOSNODE'',''SOS Node'')
	,(''MEMORYCLERK_SOSOS'',''SOS Memory Clerk'')
	,(''MEMORYCLERK_SQLBUFFERPOOL'',''Buffer Pool'')
	,(''MEMORYCLERK_SQLCLR'',''CLR'')
	,(''MEMORYCLERK_SQLCLRASSEMBLY'',''CLR Assembly'')
	,(''MEMORYCLERK_SQLCONNECTIONPOOL'',''Connection Pool'')
	,(''MEMORYCLERK_SQLGENERAL'',''General'')
	,(''MEMORYCLERK_SQLHTTP'',''HTTP'')
	,(''MEMORYCLERK_SQLLOGPOOL'',''Log Pool'')
	,(''MEMORYCLERK_SQLOPTIMIZER'',''SQL Optimizer'')
	,(''MEMORYCLERK_SQLQERESERVATIONS'',''SQL Reservations'')
	,(''MEMORYCLERK_SQLQUERYCOMPILE'',''SQL Query Compile'')
	,(''MEMORYCLERK_SQLQUERYEXEC'',''SQL Query Exec'')
	,(''MEMORYCLERK_SQLQUERYPLAN'',''SQL Query Plan'')
	,(''MEMORYCLERK_SQLSERVICEBROKER'',''SQL Service Broker'')
	,(''MEMORYCLERK_SQLSERVICEBROKERTRANSPORT'',''Unified Communication Stack'')
	,(''MEMORYCLERK_SQLSOAP'',''SQL SOAP'')
	,(''MEMORYCLERK_SQLSOAPSESSIONSTORE'',''SQL SOAP (Session Store)'')
	,(''MEMORYCLERK_SQLSTORENG'',''SQL Storage Engine'')
	,(''MEMORYCLERK_SQLUTILITIES'',''SQL Utilities'')
	,(''MEMORYCLERK_SQLXML'',''SQL XML'')
	,(''MEMORYCLERK_SQLXP'',''SQL XP'')
	,(''MEMORYCLERK_TRACE_EVTNOTIF'',''Trace Event Notification'')
	,(''MEMORYCLERK_XE'',''XE Engine'')
	,(''MEMORYCLERK_XE_BUFFER'',''XE Buffer'')
	,(''MEMORYCLERK_XTP'',''In-Memory OLTP'')
	,(''OBJECTSTORE_LBSS'',''Lbss Cache (Object Store)'')
	,(''OBJECTSTORE_LOCK_MANAGER'',''Lock Manager (Object Store)'')
	,(''OBJECTSTORE_SECAUDIT_EVENT_BUFFER'',''Audit Event Buffer (Object Store)'')
	,(''OBJECTSTORE_SERVICE_BROKER'',''Service Broker (Object Store)'')
	,(''OBJECTSTORE_SNI_PACKET'',''SNI Packet (Object Store)'')
	,(''OBJECTSTORE_XACT_CACHE'',''Transactions Cache (Object Store)'')
	,(''USERSTORE_DBMETADATA'',''DB Metadata (User Store)'')
	,(''USERSTORE_OBJPERM'',''Object Permissions (User Store)'')
	,(''USERSTORE_SCHEMAMGR'',''Schema Manager (User Store)'')
	,(''USERSTORE_SXC'',''SXC (User Store)'')
	,(''USERSTORE_TOKENPERM'',''Token Permissions (User Store)'')
	,(''USERSTORE_QDSSTMT'',''QDS Statement Buffer (Pre-persist)'')
	,(''CACHESTORE_QDSRUNTIMESTATS'',''QDS Runtime Stats (Pre-persist)'')
	,(''CACHESTORE_QDSCONTEXTSETTINGS'',''QDS Unique Context Settings'')
	,(''MEMORYCLERK_QUERYDISKSTORE'',''QDS General'')
	,(''MEMORYCLERK_QUERYDISKSTORE_HASHMAP'',''QDS Query/Plan Hash Table'')
) AS clerk_names([system_name],[name])
	ON mc.[type] = clerk_names.[system_name]
GROUP BY 
	ISNULL(clerk_names.[name], mc.[type])
HAVING 
	SUM(' + @Columns + N') >= 1024
OPTION(RECOMPILE);
'

EXEC(@SqlStatement)
`

// Conditional check based on Azure SQL DB OR On-prem SQL Server
// EngineEdition=5 is Azure SQL DB
const sqlDatabaseIOV2 = `
SET DEADLOCK_PRIORITY -10;
DECLARE 
	 @SqlStatement AS nvarchar(max)
	,@EngineEdition AS tinyint = CAST(SERVERPROPERTY('EngineEdition') AS int)

IF @EngineEdition = 5
BEGIN
	SET @SqlStatement = '
	SELECT
		 ''sqlserver_database_io'' As [measurement]
		,REPLACE(@@SERVERNAME,''\'','':'') AS [sql_instance]
		,DB_NAME() as database_name
		,vfs.database_id   -- /*needed as tempdb is different for each Azure SQL DB as grouping has to be by logical server + db_name + database_id*/
		,vfs.file_id
		,vfs.io_stall_read_ms AS read_latency_ms
		,vfs.num_of_reads AS reads
		,vfs.num_of_bytes_read AS read_bytes
		,vfs.io_stall_write_ms AS write_latency_ms
		,vfs.num_of_writes AS writes
		,vfs.num_of_bytes_written AS write_bytes
		,vfs.io_stall_queued_read_ms AS [rg_read_stall_ms]
                ,vfs.io_stall_queued_write_ms AS [rg_write_stall_ms]
		 ,CASE
                        WHEN (vfs.database_id = 0) THEN ''RBPEX''
                        ELSE b.logical_filename
                  END as logical_filename
                 ,CASE
                        WHEN (vfs.database_id = 0) THEN ''RBPEX''
                        ELSE b.physical_filename
                  END as physical_filename
		,CASE WHEN vfs.file_id = 2 THEN ''LOG'' ELSE ''DATA'' END AS file_type
		,ISNULL(size,0)/128 AS current_size_mb
		,ISNULL(FILEPROPERTY(b.logical_filename,''SpaceUsed'')/128,0) as space_used_mb
	FROM [sys].[dm_io_virtual_file_stats](NULL,NULL) AS vfs
	-- needed to get Tempdb file names  on Azure SQL DB so you can join appropriately. Without this had a bug where join was only on file_id
        LEFT OUTER join
        (
             SELECT DB_ID() as database_id, file_id, logical_filename=name COLLATE SQL_Latin1_General_CP1_CI_AS
                , physical_filename = physical_name COLLATE SQL_Latin1_General_CP1_CI_AS, size from  sys.database_files
                where type <> 2
             UNION ALL
             SELECT 2 as database_id, file_id, logical_filename = name , physical_filename = physical_name, size
                from  tempdb.sys.database_files
         ) b ON b.database_id = vfs.database_id and b.file_id = vfs.file_id
          where vfs.database_id IN (DB_ID(),0,2)
	'
	EXEC sp_executesql @SqlStatement

END
ELSE IF @EngineEdition IN (2,3,4) /*Standard,Enterprise,Express*/
BEGIN

	DECLARE @MajorMinorVersion AS int = CAST(PARSENAME(CAST(SERVERPROPERTY('ProductVersion') as nvarchar),4) AS int) * 100 + CAST(PARSENAME(CAST(SERVERPROPERTY('ProductVersion') as nvarchar),3) AS int)

	DECLARE @Columns as nvarchar(max) = ''
	DECLARE @Tables as nvarchar(max) = ''

	IF @MajorMinorVersion >= 1050 BEGIN 
		/*in [volume_mount_point] any trailing "\" char will be removed by telegraf */
		SET @Columns += N',[volume_mount_point]'
		SET @Tables += N'CROSS APPLY sys.dm_os_volume_stats(vfs.[database_id], vfs.[file_id]) AS vs'
	END
		
	IF @MajorMinorVersion > 1100 BEGIN
		SET @Columns += N'
,vfs.io_stall_queued_read_ms AS [rg_read_stall_ms] 
,vfs.io_stall_queued_write_ms AS [rg_write_stall_ms]'
	END
	
	SET @SqlStatement = N'
	SELECT
		''sqlserver_database_io'' AS [measurement]
		,REPLACE(@@SERVERNAME,''\'','':'') AS [sql_instance]
		,DB_NAME(vfs.[database_id]) AS [database_name]
		,COALESCE(mf.[physical_name],''RBPEX'') AS [physical_filename]	--RPBEX = Resilient Buffer Pool Extension
		,COALESCE(mf.[name],''RBPEX'') AS [logical_filename]	--RPBEX = Resilient Buffer Pool Extension	
		,mf.[type_desc] AS [file_type]
		,vfs.[io_stall_read_ms] AS [read_latency_ms]
		,vfs.[num_of_reads] AS [reads]
		,vfs.[num_of_bytes_read] AS [read_bytes]
		,vfs.[io_stall_write_ms] AS [write_latency_ms]
		,vfs.[num_of_writes] AS [writes]
		,vfs.[num_of_bytes_written] AS [write_bytes]'
		+ @Columns + N'
	FROM sys.dm_io_virtual_file_stats(NULL, NULL) AS vfs
	INNER JOIN sys.master_files AS mf WITH (NOLOCK)
		ON vfs.[database_id] = mf.[database_id] AND vfs.[file_id] = mf.[file_id]
	'
	+ @Tables;
	
	EXEC sp_executesql @SqlStatement

END
`

// Conditional check based on Azure SQL DB, Azure SQL Managed instance OR On-prem SQL Server
// EngineEdition=5 is Azure SQL DB, EngineEdition=8 is Managed instance

const sqlServerPropertiesV2 = `
SET DEADLOCK_PRIORITY -10;
DECLARE
	 @SqlStatement AS nvarchar(max)
	,@EngineEdition AS tinyint = CAST(SERVERPROPERTY('EngineEdition') AS int)

DECLARE @sys_info TABLE (
	cpu_count INT,
	server_memory BIGINT,
	sku NVARCHAR(64),
	engine_edition SMALLINT,
	hardware_type VARCHAR(16),
	total_storage_mb BIGINT,
	available_storage_mb BIGINT,
	uptime INT,
	affinity_type INT,
	affinity_type_desc NVARCHAR(120),
	socket_count INT,
	cores_per_socket INT,
	numa_node_count INT
)

IF @EngineEdition = 8  /*Managed Instance*/
 	INSERT INTO @sys_info ( cpu_count, server_memory, sku, engine_edition, hardware_type, total_storage_mb, available_storage_mb, uptime )
	SELECT 	TOP(1)
			virtual_core_count AS cpu_count,
			(SELECT process_memory_limit_mb FROM sys.dm_os_job_object) AS server_memory,
			sku,
			cast(SERVERPROPERTY('EngineEdition') as smallint) AS engine_edition,
			hardware_generation AS hardware_type,
			reserved_storage_mb AS total_storage_mb,
			(reserved_storage_mb - storage_space_used_mb) AS available_storage_mb,
			(select DATEDIFF(MINUTE,sqlserver_start_time,GETDATE()) from sys.dm_os_sys_info) as uptime
	FROM	sys.server_resource_stats
	ORDER BY start_time DESC

IF @EngineEdition = 5  /*Azure SQL DB*/

	INSERT INTO @sys_info ( cpu_count, server_memory, sku, engine_edition, hardware_type, total_storage_mb, available_storage_mb, uptime )
	SELECT 	TOP(1)
			(SELECT count(*) FROM sys.dm_os_schedulers WHERE status = 'VISIBLE ONLINE') AS cpu_count,
			(SELECT process_memory_limit_mb FROM sys.dm_os_job_object) AS server_memory,
			slo.edition as sku,
			cast(SERVERPROPERTY('EngineEdition') as smallint)  AS engine_edition,
			slo.service_objective AS hardware_type,
                        cast(DATABASEPROPERTYEX(DB_NAME(),'MaxSizeInBytes') as bigint)/(1024*1024)  AS total_storage_mb,
			NULL AS available_storage_mb,  -- Can we find out storage?
			NULL as uptime
	FROM	 sys.databases d   
		-- sys.databases.database_id may not match current DB_ID on Azure SQL DB
		CROSS JOIN sys.database_service_objectives slo
		WHERE d.name = DB_NAME() AND slo.database_id = DB_ID()

ELSE IF @EngineEdition IN (2,3,4) /*Standard,Enterprise,Express*/
BEGIN

	DECLARE @MajorMinorVersion AS int = CAST(PARSENAME(CAST(SERVERPROPERTY('ProductVersion') as nvarchar),4) AS int)*100 + CAST(PARSENAME(CAST(SERVERPROPERTY('ProductVersion') as nvarchar),3) AS int)
	DECLARE @Columns AS nvarchar(MAX) = ''

	IF @MajorMinorVersion >= 1050
		SET @Columns = N',CASE [virtual_machine_type_desc] 
			WHEN ''NONE'' THEN ''PHYSICAL Machine''
			ELSE [virtual_machine_type_desc]
		END AS [hardware_type],';
	ELSE /*data not available*/
		SET @Columns = N',''<n/a>'' AS [hardware_type],';

	SET @SqlStatement = '
	SELECT
		 [cpu_count]
		,(SELECT [total_physical_memory_kb] FROM sys.[dm_os_sys_memory]) AS [server_memory]
		,CAST(SERVERPROPERTY(''Edition'') AS NVARCHAR) AS [sku]
		,@EngineEdition AS [engine_edition]
		,DATEDIFF(MINUTE,[sqlserver_start_time],GETDATE()) AS [uptime]
		' + @Columns + '
		affinity_type,
		affinity_type_desc,
		socket_count,
		cores_per_socket,
		numa_node_count
	FROM sys.[dm_os_sys_info]'

	/*Insert the dynamic sql result into the table variable*/
	INSERT INTO @sys_info ( [cpu_count], [server_memory], [sku], [engine_edition], [uptime], [hardware_type], [affinity_type], [affinity_type_desc], [socket_count], [cores_per_socket], [numa_node_count]) 
	EXEC sp_executesql @SqlStatement , N'@EngineEdition smallint', @EngineEdition = @EngineEdition
END

SELECT	'sqlserver_server_properties' AS [measurement],
		REPLACE(@@SERVERNAME,'\',':') AS [sql_instance],
		cpu_count,
		server_memory,
		sku,
		engine_edition,
		hardware_type,
		total_storage_mb,
		available_storage_mb,
		uptime,
		SERVERPROPERTY('ProductVersion') AS sql_version,
		affinity_type,
		affinity_type_desc,
		socket_count,
		cores_per_socket,
		numa_node_count
FROM @sys_info
`

//Recommend disabling this by default, but is useful to detect single CPU spikes/bottlenecks
const sqlServerSchedulersV2 string = `
SET DEADLOCK_PRIORITY - 10;

DECLARE
	 @SqlStatement AS nvarchar(max)
	,@MajorMinorVersion AS int = CAST(PARSENAME(CAST(SERVERPROPERTY('ProductVersion') as nvarchar),4) AS int)*100 + CAST(PARSENAME(CAST(SERVERPROPERTY('ProductVersion') as nvarchar),3) AS int)
	,@Columns AS nvarchar(MAX) = ''

	IF @MajorMinorVersion >= 1300 BEGIN
		SET @Columns += N',s.[total_cpu_usage_ms]
	,s.[total_scheduler_delay_ms]'
	END

SET @SqlStatement = N'
SELECT 
	 ''sqlserver_schedulers'' AS [measurement]
	,REPLACE(@@SERVERNAME, ''\'', '':'') AS [sql_instance]
	,DB_NAME() AS [database_name]
	,cast(s.[scheduler_id] AS VARCHAR(4)) AS [scheduler_id]
	,cast(s.[cpu_id] AS VARCHAR(4)) AS [cpu_id]
	,s.[is_online]
	,s.[is_idle]
	,s.[preemptive_switches_count]
	,s.[context_switches_count]
	,s.[current_tasks_count]
	,s.[runnable_tasks_count]
	,s.[current_workers_count]
	,s.[active_workers_count]
	,s.[work_queue_count]
	,s.[pending_disk_io_count]
	,s.[load_factor]
	,s.[yield_count]
	' + @Columns + N'
FROM sys.dm_os_schedulers AS s'

EXEC sp_executesql @SqlStatement
`

const sqlPerformanceCountersV2 string = `
SET DEADLOCK_PRIORITY -10;

DECLARE
	 @SqlStatement AS nvarchar(max)
	,@EngineEdition AS tinyint = CAST(SERVERPROPERTY('EngineEdition') AS int)
	,@MajorMinorVersion AS int = CAST(PARSENAME(CAST(SERVERPROPERTY('ProductVersion') as nvarchar),4) AS int)*100 + CAST(PARSENAME(CAST(SERVERPROPERTY('ProductVersion') as nvarchar),3) AS int)
	,@Columns AS nvarchar(MAX) = ''
	,@PivotColumns AS nvarchar(MAX) = ''

DECLARE @PCounters TABLE
(
	object_name nvarchar(128),
	counter_name nvarchar(128),
	instance_name nvarchar(128),
	cntr_value bigint,
	cntr_type INT,
	Primary Key(object_name, counter_name, instance_name)
);

SET @SqlStatement = N'SELECT DISTINCT
		RTrim(spi.object_name) object_name,
		RTrim(spi.counter_name) counter_name,'
		+
		CASE
		WHEN @EngineEdition IN (5,8)  --- needed to get actual DB Name for SQL DB/ Managed instance
		THEN N'CASE WHEN (
                             RTRIM(spi.object_name) LIKE ''%:Databases''
                             OR RTRIM(spi.object_name) LIKE ''%:Database Replica''
                             OR RTRIM(spi.object_name) LIKE ''%:Catalog Metadata''
                             OR RTRIM(spi.object_name) LIKE ''%:Query Store''
                             OR RTRIM(spi.object_name) LIKE ''%:Columnstore''
                             OR RTRIM(spi.object_name) LIKE ''%:Advanced Analytics'')
                             AND TRY_CONVERT(uniqueidentifier, spi.instance_name) 
							 IS NOT NULL -- for cloud only
                THEN ISNULL(d.name,RTRIM(spi.instance_name)) -- Elastic Pools counters exist for all databases but sys.databases only has current DB value
			  WHEN RTRIM(object_name) LIKE ''%:Availability Replica''
				AND TRY_CONVERT(uniqueidentifier, spi.instance_name) IS NOT NULL -- for cloud only
			THEN ISNULL(d.name,RTRIM(spi.instance_name)) + RTRIM(SUBSTRING(spi.instance_name, 37, LEN(spi.instance_name)))
                       ELSE RTRIM(spi.instance_name)
                END AS instance_name,'
		ELSE 'RTRIM(spi.instance_name) as instance_name, '
		END
		+
		'CAST(spi.cntr_value AS BIGINT) AS cntr_value,
		spi.cntr_type
		FROM	sys.dm_os_performance_counters AS spi '
+
CASE 
	WHEN @EngineEdition IN (5,8)  --- Join is ONLY for managed instance and SQL DB, not for on-prem
	THEN CAST(N'LEFT JOIN sys.databases AS d
	ON LEFT(spi.instance_name, 36) -- some instance_name values have an additional identifier appended after the GUID
	= CASE WHEN -- in SQL DB standalone, physical_database_name for master is the GUID of the user database
                d.name = ''master'' AND TRY_CONVERT(uniqueidentifier, d.physical_database_name) IS NOT NULL
                THEN d.name
                ELSE d.physical_database_name
	END	' as NVARCHAR(MAX))
	ELSE N''
END

SET @SqlStatement = @SqlStatement + CAST(N' WHERE	(
			counter_name IN (
				''SQL Compilations/sec'',
				''SQL Re-Compilations/sec'',
				''User Connections'',
				''Batch Requests/sec'',
				''Logouts/sec'',
				''Logins/sec'',
				''Processes blocked'',
				''Latch Waits/sec'',
				''Full Scans/sec'',
				''Index Searches/sec'',
				''Page Splits/sec'',
				''Page lookups/sec'',
				''Page reads/sec'',
				''Page writes/sec'',
				''Readahead pages/sec'',
				''Lazy writes/sec'',
				''Checkpoint pages/sec'',
				''Page life expectancy'',
				''Log File(s) Size (KB)'',
				''Log File(s) Used Size (KB)'',
				''Data File(s) Size (KB)'',
				''Transactions/sec'',
				''Write Transactions/sec'',
				''Active Temp Tables'',
				''Temp Tables Creation Rate'',
				''Temp Tables For Destruction'',
				''Free Space in tempdb (KB)'',
				''Version Store Size (KB)'',
				''Memory Grants Pending'',
				''Memory Grants Outstanding'',
				''Free list stalls/sec'',
				''Buffer cache hit ratio'',
				''Buffer cache hit ratio base'',
				''Backup/Restore Throughput/sec'',
				''Total Server Memory (KB)'',
				''Target Server Memory (KB)'',
				''Log Flushes/sec'',
				''Log Flush Wait Time'',
				''Memory broker clerk size'',
				''Log Bytes Flushed/sec'',
				''Bytes Sent to Replica/sec'',
				''Log Send Queue'',
				''Bytes Sent to Transport/sec'',
				''Sends to Replica/sec'',
				''Bytes Sent to Transport/sec'',
				''Sends to Transport/sec'',
				''Bytes Received from Replica/sec'',
				''Receives from Replica/sec'',
				''Flow Control Time (ms/sec)'',
				''Flow Control/sec'',
				''Resent Messages/sec'',
				''Redone Bytes/sec'',
				''XTP Memory Used (KB)'',
				''Transaction Delay'',
				''Log Bytes Received/sec'',
				''Log Apply Pending Queue'',
				''Redone Bytes/sec'',
				''Recovery Queue'',
				''Log Apply Ready Queue'',
				''CPU usage %'',
				''CPU usage % base'',
				''Queued requests'',
				''Requests completed/sec'',
				''Blocked tasks'',
				''Active memory grant amount (KB)'',
				''Disk Read Bytes/sec'',
				''Disk Read IO Throttled/sec'',
				''Disk Read IO/sec'',
				''Disk Write Bytes/sec'',
				''Disk Write IO Throttled/sec'',
				''Disk Write IO/sec'',
				''Used memory (KB)'',
				''Forwarded Records/sec'',
				''Background Writer pages/sec'',
				''Percent Log Used'',
				''Log Send Queue KB'',
				''Redo Queue KB'',
				''Mirrored Write Transactions/sec'',
				''Group Commit Time'',
				''Group Commits/Sec''
			)
		) OR (
			object_name LIKE ''%User Settable%''
			OR object_name LIKE ''%SQL Errors%''
		) OR (
			object_name LIKE ''%Batch Resp Statistics%''
		) OR (
			instance_name IN (''_Total'')
			AND counter_name IN (
				''Lock Timeouts/sec'',
				''Number of Deadlocks/sec'',
				''Lock Waits/sec'',
				''Latch Waits/sec''
			)
		)
' as NVARCHAR(MAX))
INSERT	INTO @PCounters
EXEC (@SqlStatement)

IF @MajorMinorVersion >= 1300 BEGIN
	SET @Columns += N',rgwg.[total_cpu_usage_preemptive_ms] AS [Preemptive CPU Usage (time)]'
	SET @PivotColumns += N',[Preemptive CPU Usage (time)]'
END

SET  @SqlStatement = N'
SELECT
	 ''SQLServer:Workload Group Stats'' AS [object]
	,[counter]
	,[instance]
	,CAST(vs.[value] AS BIGINT) AS [value]
	,1
FROM
(
    SELECT
		 rgwg.name AS instance
		,rgwg.total_request_count AS [Request Count]
		,rgwg.total_queued_request_count AS [Queued Request Count]
		,rgwg.total_cpu_limit_violation_count AS [CPU Limit Violation Count]
		,rgwg.total_cpu_usage_ms AS [CPU Usage (time)]
		,rgwg.total_lock_wait_count AS [Lock Wait Count]
		,rgwg.total_lock_wait_time_ms AS [Lock Wait Time]
		,rgwg.total_reduced_memgrant_count AS [Reduced Memory Grant Count]
		' + @Columns + N'
	FROM sys.[dm_resource_governor_workload_groups] AS rgwg
	INNER JOIN sys.[dm_resource_governor_resource_pools] AS rgrp
		ON rgwg.[pool_id] = rgrp.[pool_id]
) AS rg
UNPIVOT (
    value FOR counter IN ( [Request Count], [Queued Request Count], [CPU Limit Violation Count], [CPU Usage (time)], [Lock Wait Count], [Lock Wait Time], [Reduced Memory Grant Count] ' + @PivotColumns + N')
) AS vs'

INSERT INTO @PCounters
EXEC( @SqlStatement )

SELECT	'sqlserver_performance' AS [measurement],
		REPLACE(@@SERVERNAME,'\',':') AS [sql_instance],
		pc.object_name AS [object],
		pc.counter_name AS [counter],
		CASE pc.instance_name WHEN '_Total' THEN 'Total' ELSE ISNULL(pc.instance_name,'') END AS [instance],
		CAST(CASE WHEN pc.cntr_type = 537003264 AND pc1.cntr_value > 0 THEN (pc.cntr_value * 1.0) / (pc1.cntr_value * 1.0) * 100 ELSE pc.cntr_value END AS float(10)) AS [value]
FROM	@PCounters AS pc
		LEFT OUTER JOIN @PCounters AS pc1
			ON (
				pc.counter_name = REPLACE(pc1.counter_name,' base','')
				OR pc.counter_name = REPLACE(pc1.counter_name,' base',' (ms)')
			)
			AND pc.object_name = pc1.object_name
			AND pc.instance_name = pc1.instance_name
			AND pc1.counter_name LIKE '%base'
WHERE	pc.counter_name NOT LIKE '% base'
OPTION(RECOMPILE);
`

// Conditional check based on Azure SQL DB v/s the rest aka (Azure SQL Managed instance OR On-prem SQL Server)
// EngineEdition=5 is Azure SQL DB
const sqlWaitStatsCategorizedV2 string = `
SET DEADLOCK_PRIORITY -10;

IF SERVERPROPERTY('EngineEdition') != 5
SELECT
	'sqlserver_waitstats' AS [measurement],
REPLACE(@@SERVERNAME,'\',':') AS [sql_instance],
DB_NAME() as [database_name],
ws.wait_type,
wait_time_ms,
wait_time_ms - signal_wait_time_ms AS [resource_wait_ms],
signal_wait_time_ms,
max_wait_time_ms,
waiting_tasks_count,
ISNULL(wc.wait_category,'OTHER') AS [wait_category]
FROM
sys.dm_os_wait_stats AS ws WITH (NOLOCK)
LEFT OUTER JOIN ( VALUES
('ASYNC_IO_COMPLETION','Other Disk IO'),
('ASYNC_NETWORK_IO','Network IO'),
('BACKUPIO','Other Disk IO'),
('BROKER_CONNECTION_RECEIVE_TASK','Service Broker'),
('BROKER_DISPATCHER','Service Broker'),
('BROKER_ENDPOINT_STATE_MUTEX','Service Broker'),
('BROKER_EVENTHANDLER','Service Broker'),
('BROKER_FORWARDER','Service Broker'),
('BROKER_INIT','Service Broker'),
('BROKER_MASTERSTART','Service Broker'),
('BROKER_RECEIVE_WAITFOR','User Wait'),
('BROKER_REGISTERALLENDPOINTS','Service Broker'),
('BROKER_SERVICE','Service Broker'),
('BROKER_SHUTDOWN','Service Broker'),
('BROKER_START','Service Broker'),
('BROKER_TASK_SHUTDOWN','Service Broker'),
('BROKER_TASK_STOP','Service Broker'),
('BROKER_TASK_SUBMIT','Service Broker'),
('BROKER_TO_FLUSH','Service Broker'),
('BROKER_TRANSMISSION_OBJECT','Service Broker'),
('BROKER_TRANSMISSION_TABLE','Service Broker'),
('BROKER_TRANSMISSION_WORK','Service Broker'),
('BROKER_TRANSMITTER','Service Broker'),
('CHECKPOINT_QUEUE','Idle'),
('CHKPT','Tran Log IO'),
('CLR_AUTO_EVENT','SQL CLR'),
('CLR_CRST','SQL CLR'),
('CLR_JOIN','SQL CLR'),
('CLR_MANUAL_EVENT','SQL CLR'),
('CLR_MEMORY_SPY','SQL CLR'),
('CLR_MONITOR','SQL CLR'),
('CLR_RWLOCK_READER','SQL CLR'),
('CLR_RWLOCK_WRITER','SQL CLR'),
('CLR_SEMAPHORE','SQL CLR'),
('CLR_TASK_START','SQL CLR'),
('CLRHOST_STATE_ACCESS','SQL CLR'),
('CMEMPARTITIONED','Memory'),
('CMEMTHREAD','Memory'),
('CXPACKET','Parallelism'),
('CXCONSUMER','Parallelism'),
('DBMIRROR_DBM_EVENT','Mirroring'),
('DBMIRROR_DBM_MUTEX','Mirroring'),
('DBMIRROR_EVENTS_QUEUE','Mirroring'),
('DBMIRROR_SEND','Mirroring'),
('DBMIRROR_WORKER_QUEUE','Mirroring'),
('DBMIRRORING_CMD','Mirroring'),
('DTC','Transaction'),
('DTC_ABORT_REQUEST','Transaction'),
('DTC_RESOLVE','Transaction'),
('DTC_STATE','Transaction'),
('DTC_TMDOWN_REQUEST','Transaction'),
('DTC_WAITFOR_OUTCOME','Transaction'),
('DTCNEW_ENLIST','Transaction'),
('DTCNEW_PREPARE','Transaction'),
('DTCNEW_RECOVERY','Transaction'),
('DTCNEW_TM','Transaction'),
('DTCNEW_TRANSACTION_ENLISTMENT','Transaction'),
('DTCPNTSYNC','Transaction'),
('EE_PMOLOCK','Memory'),
('EXCHANGE','Parallelism'),
('EXTERNAL_SCRIPT_NETWORK_IOF','Network IO'),
('FCB_REPLICA_READ','Replication'),
('FCB_REPLICA_WRITE','Replication'),
('FT_COMPROWSET_RWLOCK','Full Text Search'),
('FT_IFTS_RWLOCK','Full Text Search'),
('FT_IFTS_SCHEDULER_IDLE_WAIT','Idle'),
('FT_IFTSHC_MUTEX','Full Text Search'),
('FT_IFTSISM_MUTEX','Full Text Search'),
('FT_MASTER_MERGE','Full Text Search'),
('FT_MASTER_MERGE_COORDINATOR','Full Text Search'),
('FT_METADATA_MUTEX','Full Text Search'),
('FT_PROPERTYLIST_CACHE','Full Text Search'),
('FT_RESTART_CRAWL','Full Text Search'),
('FULLTEXT GATHERER','Full Text Search'),
('HADR_AG_MUTEX','Replication'),
('HADR_AR_CRITICAL_SECTION_ENTRY','Replication'),
('HADR_AR_MANAGER_MUTEX','Replication'),
('HADR_AR_UNLOAD_COMPLETED','Replication'),
('HADR_ARCONTROLLER_NOTIFICATIONS_SUBSCRIBER_LIST','Replication'),
('HADR_BACKUP_BULK_LOCK','Replication'),
('HADR_BACKUP_QUEUE','Replication'),
('HADR_CLUSAPI_CALL','Replication'),
('HADR_COMPRESSED_CACHE_SYNC','Replication'),
('HADR_CONNECTIVITY_INFO','Replication'),
('HADR_DATABASE_FLOW_CONTROL','Replication'),
('HADR_DATABASE_VERSIONING_STATE','Replication'),
('HADR_DATABASE_WAIT_FOR_RECOVERY','Replication'),
('HADR_DATABASE_WAIT_FOR_RESTART','Replication'),
('HADR_DATABASE_WAIT_FOR_TRANSITION_TO_VERSIONING','Replication'),
('HADR_DB_COMMAND','Replication'),
('HADR_DB_OP_COMPLETION_SYNC','Replication'),
('HADR_DB_OP_START_SYNC','Replication'),
('HADR_DBR_SUBSCRIBER','Replication'),
('HADR_DBR_SUBSCRIBER_FILTER_LIST','Replication'),
('HADR_DBSEEDING','Replication'),
('HADR_DBSEEDING_LIST','Replication'),
('HADR_DBSTATECHANGE_SYNC','Replication'),
('HADR_FABRIC_CALLBACK','Replication'),
('HADR_FILESTREAM_BLOCK_FLUSH','Replication'),
('HADR_FILESTREAM_FILE_CLOSE','Replication'),
('HADR_FILESTREAM_FILE_REQUEST','Replication'),
('HADR_FILESTREAM_IOMGR','Replication'),
('HADR_FILESTREAM_IOMGR_IOCOMPLETION','Replication'),
('HADR_FILESTREAM_MANAGER','Replication'),
('HADR_FILESTREAM_PREPROC','Replication'),
('HADR_GROUP_COMMIT','Replication'),
('HADR_LOGCAPTURE_SYNC','Replication'),
('HADR_LOGCAPTURE_WAIT','Replication'),
('HADR_LOGPROGRESS_SYNC','Replication'),
('HADR_NOTIFICATION_DEQUEUE','Replication'),
('HADR_NOTIFICATION_WORKER_EXCLUSIVE_ACCESS','Replication'),
('HADR_NOTIFICATION_WORKER_STARTUP_SYNC','Replication'),
('HADR_NOTIFICATION_WORKER_TERMINATION_SYNC','Replication'),
('HADR_PARTNER_SYNC','Replication'),
('HADR_READ_ALL_NETWORKS','Replication'),
('HADR_RECOVERY_WAIT_FOR_CONNECTION','Replication'),
('HADR_RECOVERY_WAIT_FOR_UNDO','Replication'),
('HADR_REPLICAINFO_SYNC','Replication'),
('HADR_SEEDING_CANCELLATION','Replication'),
('HADR_SEEDING_FILE_LIST','Replication'),
('HADR_SEEDING_LIMIT_BACKUPS','Replication'),
('HADR_SEEDING_SYNC_COMPLETION','Replication'),
('HADR_SEEDING_TIMEOUT_TASK','Replication'),
('HADR_SEEDING_WAIT_FOR_COMPLETION','Replication'),
('HADR_SYNC_COMMIT','Replication'),
('HADR_SYNCHRONIZING_THROTTLE','Replication'),
('HADR_TDS_LISTENER_SYNC','Replication'),
('HADR_TDS_LISTENER_SYNC_PROCESSING','Replication'),
('HADR_THROTTLE_LOG_RATE_GOVERNOR','Log Rate Governor'),
('HADR_TIMER_TASK','Replication'),
('HADR_TRANSPORT_DBRLIST','Replication'),
('HADR_TRANSPORT_FLOW_CONTROL','Replication'),
('HADR_TRANSPORT_SESSION','Replication'),
('HADR_WORK_POOL','Replication'),
('HADR_WORK_QUEUE','Replication'),
('HADR_XRF_STACK_ACCESS','Replication'),
('INSTANCE_LOG_RATE_GOVERNOR','Log Rate Governor'),
('IO_COMPLETION','Other Disk IO'),
('IO_QUEUE_LIMIT','Other Disk IO'),
('IO_RETRY','Other Disk IO'),
('LATCH_DT','Latch'),
('LATCH_EX','Latch'),
('LATCH_KP','Latch'),
('LATCH_NL','Latch'),
('LATCH_SH','Latch'),
('LATCH_UP','Latch'),
('LAZYWRITER_SLEEP','Idle'),
('LCK_M_BU','Lock'),
('LCK_M_BU_ABORT_BLOCKERS','Lock'),
('LCK_M_BU_LOW_PRIORITY','Lock'),
('LCK_M_IS','Lock'),
('LCK_M_IS_ABORT_BLOCKERS','Lock'),
('LCK_M_IS_LOW_PRIORITY','Lock'),
('LCK_M_IU','Lock'),
('LCK_M_IU_ABORT_BLOCKERS','Lock'),
('LCK_M_IU_LOW_PRIORITY','Lock'),
('LCK_M_IX','Lock'),
('LCK_M_IX_ABORT_BLOCKERS','Lock'),
('LCK_M_IX_LOW_PRIORITY','Lock'),
('LCK_M_RIn_NL','Lock'),
('LCK_M_RIn_NL_ABORT_BLOCKERS','Lock'),
('LCK_M_RIn_NL_LOW_PRIORITY','Lock'),
('LCK_M_RIn_S','Lock'),
('LCK_M_RIn_S_ABORT_BLOCKERS','Lock'),
('LCK_M_RIn_S_LOW_PRIORITY','Lock'),
('LCK_M_RIn_U','Lock'),
('LCK_M_RIn_U_ABORT_BLOCKERS','Lock'),
('LCK_M_RIn_U_LOW_PRIORITY','Lock'),
('LCK_M_RIn_X','Lock'),
('LCK_M_RIn_X_ABORT_BLOCKERS','Lock'),
('LCK_M_RIn_X_LOW_PRIORITY','Lock'),
('LCK_M_RS_S','Lock'),
('LCK_M_RS_S_ABORT_BLOCKERS','Lock'),
('LCK_M_RS_S_LOW_PRIORITY','Lock'),
('LCK_M_RS_U','Lock'),
('LCK_M_RS_U_ABORT_BLOCKERS','Lock'),
('LCK_M_RS_U_LOW_PRIORITY','Lock'),
('LCK_M_RX_S','Lock'),
('LCK_M_RX_S_ABORT_BLOCKERS','Lock'),
('LCK_M_RX_S_LOW_PRIORITY','Lock'),
('LCK_M_RX_U','Lock'),
('LCK_M_RX_U_ABORT_BLOCKERS','Lock'),
('LCK_M_RX_U_LOW_PRIORITY','Lock'),
('LCK_M_RX_X','Lock'),
('LCK_M_RX_X_ABORT_BLOCKERS','Lock'),
('LCK_M_RX_X_LOW_PRIORITY','Lock'),
('LCK_M_S','Lock'),
('LCK_M_S_ABORT_BLOCKERS','Lock'),
('LCK_M_S_LOW_PRIORITY','Lock'),
('LCK_M_SCH_M','Lock'),
('LCK_M_SCH_M_ABORT_BLOCKERS','Lock'),
('LCK_M_SCH_M_LOW_PRIORITY','Lock'),
('LCK_M_SCH_S','Lock'),
('LCK_M_SCH_S_ABORT_BLOCKERS','Lock'),
('LCK_M_SCH_S_LOW_PRIORITY','Lock'),
('LCK_M_SIU','Lock'),
('LCK_M_SIU_ABORT_BLOCKERS','Lock'),
('LCK_M_SIU_LOW_PRIORITY','Lock'),
('LCK_M_SIX','Lock'),
('LCK_M_SIX_ABORT_BLOCKERS','Lock'),
('LCK_M_SIX_LOW_PRIORITY','Lock'),
('LCK_M_U','Lock'),
('LCK_M_U_ABORT_BLOCKERS','Lock'),
('LCK_M_U_LOW_PRIORITY','Lock'),
('LCK_M_UIX','Lock'),
('LCK_M_UIX_ABORT_BLOCKERS','Lock'),
('LCK_M_UIX_LOW_PRIORITY','Lock'),
('LCK_M_X','Lock'),
('LCK_M_X_ABORT_BLOCKERS','Lock'),
('LCK_M_X_LOW_PRIORITY','Lock'),
('LOGBUFFER','Tran Log IO'),
('LOGMGR','Tran Log IO'),
('LOGMGR_FLUSH','Tran Log IO'),
('LOGMGR_PMM_LOG','Tran Log IO'),
('LOGMGR_QUEUE','Idle'),
('LOGMGR_RESERVE_APPEND','Tran Log IO'),
('MEMORY_ALLOCATION_EXT','Memory'),
('MEMORY_GRANT_UPDATE','Memory'),
('MSQL_XACT_MGR_MUTEX','Transaction'),
('MSQL_XACT_MUTEX','Transaction'),
('MSSEARCH','Full Text Search'),
('NET_WAITFOR_PACKET','Network IO'),
('ONDEMAND_TASK_QUEUE','Idle'),
('PAGEIOLATCH_DT','Buffer IO'),
('PAGEIOLATCH_EX','Buffer IO'),
('PAGEIOLATCH_KP','Buffer IO'),
('PAGEIOLATCH_NL','Buffer IO'),
('PAGEIOLATCH_SH','Buffer IO'),
('PAGEIOLATCH_UP','Buffer IO'),
('PAGELATCH_DT','Buffer Latch'),
('PAGELATCH_EX','Buffer Latch'),
('PAGELATCH_KP','Buffer Latch'),
('PAGELATCH_NL','Buffer Latch'),
('PAGELATCH_SH','Buffer Latch'),
('PAGELATCH_UP','Buffer Latch'),
('POOL_LOG_RATE_GOVERNOR','Log Rate Governor'),
('PREEMPTIVE_ABR','Preemptive'),
('PREEMPTIVE_CLOSEBACKUPMEDIA','Preemptive'),
('PREEMPTIVE_CLOSEBACKUPTAPE','Preemptive'),
('PREEMPTIVE_CLOSEBACKUPVDIDEVICE','Preemptive'),
('PREEMPTIVE_CLUSAPI_CLUSTERRESOURCECONTROL','Preemptive'),
('PREEMPTIVE_COM_COCREATEINSTANCE','Preemptive'),
('PREEMPTIVE_COM_COGETCLASSOBJECT','Preemptive'),
('PREEMPTIVE_COM_CREATEACCESSOR','Preemptive'),
('PREEMPTIVE_COM_DELETEROWS','Preemptive'),
('PREEMPTIVE_COM_GETCOMMANDTEXT','Preemptive'),
('PREEMPTIVE_COM_GETDATA','Preemptive'),
('PREEMPTIVE_COM_GETNEXTROWS','Preemptive'),
('PREEMPTIVE_COM_GETRESULT','Preemptive'),
('PREEMPTIVE_COM_GETROWSBYBOOKMARK','Preemptive'),
('PREEMPTIVE_COM_LBFLUSH','Preemptive'),
('PREEMPTIVE_COM_LBLOCKREGION','Preemptive'),
('PREEMPTIVE_COM_LBREADAT','Preemptive'),
('PREEMPTIVE_COM_LBSETSIZE','Preemptive'),
('PREEMPTIVE_COM_LBSTAT','Preemptive'),
('PREEMPTIVE_COM_LBUNLOCKREGION','Preemptive'),
('PREEMPTIVE_COM_LBWRITEAT','Preemptive'),
('PREEMPTIVE_COM_QUERYINTERFACE','Preemptive'),
('PREEMPTIVE_COM_RELEASE','Preemptive'),
('PREEMPTIVE_COM_RELEASEACCESSOR','Preemptive'),
('PREEMPTIVE_COM_RELEASEROWS','Preemptive'),
('PREEMPTIVE_COM_RELEASESESSION','Preemptive'),
('PREEMPTIVE_COM_RESTARTPOSITION','Preemptive'),
('PREEMPTIVE_COM_SEQSTRMREAD','Preemptive'),
('PREEMPTIVE_COM_SEQSTRMREADANDWRITE','Preemptive'),
('PREEMPTIVE_COM_SETDATAFAILURE','Preemptive'),
('PREEMPTIVE_COM_SETPARAMETERINFO','Preemptive'),
('PREEMPTIVE_COM_SETPARAMETERPROPERTIES','Preemptive'),
('PREEMPTIVE_COM_STRMLOCKREGION','Preemptive'),
('PREEMPTIVE_COM_STRMSEEKANDREAD','Preemptive'),
('PREEMPTIVE_COM_STRMSEEKANDWRITE','Preemptive'),
('PREEMPTIVE_COM_STRMSETSIZE','Preemptive'),
('PREEMPTIVE_COM_STRMSTAT','Preemptive'),
('PREEMPTIVE_COM_STRMUNLOCKREGION','Preemptive'),
('PREEMPTIVE_CONSOLEWRITE','Preemptive'),
('PREEMPTIVE_CREATEPARAM','Preemptive'),
('PREEMPTIVE_DEBUG','Preemptive'),
('PREEMPTIVE_DFSADDLINK','Preemptive'),
('PREEMPTIVE_DFSLINKEXISTCHECK','Preemptive'),
('PREEMPTIVE_DFSLINKHEALTHCHECK','Preemptive'),
('PREEMPTIVE_DFSREMOVELINK','Preemptive'),
('PREEMPTIVE_DFSREMOVEROOT','Preemptive'),
('PREEMPTIVE_DFSROOTFOLDERCHECK','Preemptive'),
('PREEMPTIVE_DFSROOTINIT','Preemptive'),
('PREEMPTIVE_DFSROOTSHARECHECK','Preemptive'),
('PREEMPTIVE_DTC_ABORT','Preemptive'),
('PREEMPTIVE_DTC_ABORTREQUESTDONE','Preemptive'),
('PREEMPTIVE_DTC_BEGINTRANSACTION','Preemptive'),
('PREEMPTIVE_DTC_COMMITREQUESTDONE','Preemptive'),
('PREEMPTIVE_DTC_ENLIST','Preemptive'),
('PREEMPTIVE_DTC_PREPAREREQUESTDONE','Preemptive'),
('PREEMPTIVE_FILESIZEGET','Preemptive'),
('PREEMPTIVE_FSAOLEDB_ABORTTRANSACTION','Preemptive'),
('PREEMPTIVE_FSAOLEDB_COMMITTRANSACTION','Preemptive'),
('PREEMPTIVE_FSAOLEDB_STARTTRANSACTION','Preemptive'),
('PREEMPTIVE_FSRECOVER_UNCONDITIONALUNDO','Preemptive'),
('PREEMPTIVE_GETRMINFO','Preemptive'),
('PREEMPTIVE_HADR_LEASE_MECHANISM','Preemptive'),
('PREEMPTIVE_HTTP_EVENT_WAIT','Preemptive'),
('PREEMPTIVE_HTTP_REQUEST','Preemptive'),
('PREEMPTIVE_LOCKMONITOR','Preemptive'),
('PREEMPTIVE_MSS_RELEASE','Preemptive'),
('PREEMPTIVE_ODBCOPS','Preemptive'),
('PREEMPTIVE_OLE_UNINIT','Preemptive'),
('PREEMPTIVE_OLEDB_ABORTORCOMMITTRAN','Preemptive'),
('PREEMPTIVE_OLEDB_ABORTTRAN','Preemptive'),
('PREEMPTIVE_OLEDB_GETDATASOURCE','Preemptive'),
('PREEMPTIVE_OLEDB_GETLITERALINFO','Preemptive'),
('PREEMPTIVE_OLEDB_GETPROPERTIES','Preemptive'),
('PREEMPTIVE_OLEDB_GETPROPERTYINFO','Preemptive'),
('PREEMPTIVE_OLEDB_GETSCHEMALOCK','Preemptive'),
('PREEMPTIVE_OLEDB_JOINTRANSACTION','Preemptive'),
('PREEMPTIVE_OLEDB_RELEASE','Preemptive'),
('PREEMPTIVE_OLEDB_SETPROPERTIES','Preemptive'),
('PREEMPTIVE_OLEDBOPS','Preemptive'),
('PREEMPTIVE_OS_ACCEPTSECURITYCONTEXT','Preemptive'),
('PREEMPTIVE_OS_ACQUIRECREDENTIALSHANDLE','Preemptive'),
('PREEMPTIVE_OS_AUTHENTICATIONOPS','Preemptive'),
('PREEMPTIVE_OS_AUTHORIZATIONOPS','Preemptive'),
('PREEMPTIVE_OS_AUTHZGETINFORMATIONFROMCONTEXT','Preemptive'),
('PREEMPTIVE_OS_AUTHZINITIALIZECONTEXTFROMSID','Preemptive'),
('PREEMPTIVE_OS_AUTHZINITIALIZERESOURCEMANAGER','Preemptive'),
('PREEMPTIVE_OS_BACKUPREAD','Preemptive'),
('PREEMPTIVE_OS_CLOSEHANDLE','Preemptive'),
('PREEMPTIVE_OS_CLUSTEROPS','Preemptive'),
('PREEMPTIVE_OS_COMOPS','Preemptive'),
('PREEMPTIVE_OS_COMPLETEAUTHTOKEN','Preemptive'),
('PREEMPTIVE_OS_COPYFILE','Preemptive'),
('PREEMPTIVE_OS_CREATEDIRECTORY','Preemptive'),
('PREEMPTIVE_OS_CREATEFILE','Preemptive'),
('PREEMPTIVE_OS_CRYPTACQUIRECONTEXT','Preemptive'),
('PREEMPTIVE_OS_CRYPTIMPORTKEY','Preemptive'),
('PREEMPTIVE_OS_CRYPTOPS','Preemptive'),
('PREEMPTIVE_OS_DECRYPTMESSAGE','Preemptive'),
('PREEMPTIVE_OS_DELETEFILE','Preemptive'),
('PREEMPTIVE_OS_DELETESECURITYCONTEXT','Preemptive'),
('PREEMPTIVE_OS_DEVICEIOCONTROL','Preemptive'),
('PREEMPTIVE_OS_DEVICEOPS','Preemptive'),
('PREEMPTIVE_OS_DIRSVC_NETWORKOPS','Preemptive'),
('PREEMPTIVE_OS_DISCONNECTNAMEDPIPE','Preemptive'),
('PREEMPTIVE_OS_DOMAINSERVICESOPS','Preemptive'),
('PREEMPTIVE_OS_DSGETDCNAME','Preemptive'),
('PREEMPTIVE_OS_DTCOPS','Preemptive'),
('PREEMPTIVE_OS_ENCRYPTMESSAGE','Preemptive'),
('PREEMPTIVE_OS_FILEOPS','Preemptive'),
('PREEMPTIVE_OS_FINDFILE','Preemptive'),
('PREEMPTIVE_OS_FLUSHFILEBUFFERS','Preemptive'),
('PREEMPTIVE_OS_FORMATMESSAGE','Preemptive'),
('PREEMPTIVE_OS_FREECREDENTIALSHANDLE','Preemptive'),
('PREEMPTIVE_OS_FREELIBRARY','Preemptive'),
('PREEMPTIVE_OS_GENERICOPS','Preemptive'),
('PREEMPTIVE_OS_GETADDRINFO','Preemptive'),
('PREEMPTIVE_OS_GETCOMPRESSEDFILESIZE','Preemptive'),
('PREEMPTIVE_OS_GETDISKFREESPACE','Preemptive'),
('PREEMPTIVE_OS_GETFILEATTRIBUTES','Preemptive'),
('PREEMPTIVE_OS_GETFILESIZE','Preemptive'),
('PREEMPTIVE_OS_GETFINALFILEPATHBYHANDLE','Preemptive'),
('PREEMPTIVE_OS_GETLONGPATHNAME','Preemptive'),
('PREEMPTIVE_OS_GETPROCADDRESS','Preemptive'),
('PREEMPTIVE_OS_GETVOLUMENAMEFORVOLUMEMOUNTPOINT','Preemptive'),
('PREEMPTIVE_OS_GETVOLUMEPATHNAME','Preemptive'),
('PREEMPTIVE_OS_INITIALIZESECURITYCONTEXT','Preemptive'),
('PREEMPTIVE_OS_LIBRARYOPS','Preemptive'),
('PREEMPTIVE_OS_LOADLIBRARY','Preemptive'),
('PREEMPTIVE_OS_LOGONUSER','Preemptive'),
('PREEMPTIVE_OS_LOOKUPACCOUNTSID','Preemptive'),
('PREEMPTIVE_OS_MESSAGEQUEUEOPS','Preemptive'),
('PREEMPTIVE_OS_MOVEFILE','Preemptive'),
('PREEMPTIVE_OS_NETGROUPGETUSERS','Preemptive'),
('PREEMPTIVE_OS_NETLOCALGROUPGETMEMBERS','Preemptive'),
('PREEMPTIVE_OS_NETUSERGETGROUPS','Preemptive'),
('PREEMPTIVE_OS_NETUSERGETLOCALGROUPS','Preemptive'),
('PREEMPTIVE_OS_NETUSERMODALSGET','Preemptive'),
('PREEMPTIVE_OS_NETVALIDATEPASSWORDPOLICY','Preemptive'),
('PREEMPTIVE_OS_NETVALIDATEPASSWORDPOLICYFREE','Preemptive'),
('PREEMPTIVE_OS_OPENDIRECTORY','Preemptive'),
('PREEMPTIVE_OS_PDH_WMI_INIT','Preemptive'),
('PREEMPTIVE_OS_PIPEOPS','Preemptive'),
('PREEMPTIVE_OS_PROCESSOPS','Preemptive'),
('PREEMPTIVE_OS_QUERYCONTEXTATTRIBUTES','Preemptive'),
('PREEMPTIVE_OS_QUERYREGISTRY','Preemptive'),
('PREEMPTIVE_OS_QUERYSECURITYCONTEXTTOKEN','Preemptive'),
('PREEMPTIVE_OS_REMOVEDIRECTORY','Preemptive'),
('PREEMPTIVE_OS_REPORTEVENT','Preemptive'),
('PREEMPTIVE_OS_REVERTTOSELF','Preemptive'),
('PREEMPTIVE_OS_RSFXDEVICEOPS','Preemptive'),
('PREEMPTIVE_OS_SECURITYOPS','Preemptive'),
('PREEMPTIVE_OS_SERVICEOPS','Preemptive'),
('PREEMPTIVE_OS_SETENDOFFILE','Preemptive'),
('PREEMPTIVE_OS_SETFILEPOINTER','Preemptive'),
('PREEMPTIVE_OS_SETFILEVALIDDATA','Preemptive'),
('PREEMPTIVE_OS_SETNAMEDSECURITYINFO','Preemptive'),
('PREEMPTIVE_OS_SQLCLROPS','Preemptive'),
('PREEMPTIVE_OS_SQMLAUNCH','Preemptive'),
('PREEMPTIVE_OS_VERIFYSIGNATURE','Preemptive'),
('PREEMPTIVE_OS_VERIFYTRUST','Preemptive'),
('PREEMPTIVE_OS_VSSOPS','Preemptive'),
('PREEMPTIVE_OS_WAITFORSINGLEOBJECT','Preemptive'),
('PREEMPTIVE_OS_WINSOCKOPS','Preemptive'),
('PREEMPTIVE_OS_WRITEFILE','Preemptive'),
('PREEMPTIVE_OS_WRITEFILEGATHER','Preemptive'),
('PREEMPTIVE_OS_WSASETLASTERROR','Preemptive'),
('PREEMPTIVE_REENLIST','Preemptive'),
('PREEMPTIVE_RESIZELOG','Preemptive'),
('PREEMPTIVE_ROLLFORWARDREDO','Preemptive'),
('PREEMPTIVE_ROLLFORWARDUNDO','Preemptive'),
('PREEMPTIVE_SB_STOPENDPOINT','Preemptive'),
('PREEMPTIVE_SERVER_STARTUP','Preemptive'),
('PREEMPTIVE_SETRMINFO','Preemptive'),
('PREEMPTIVE_SHAREDMEM_GETDATA','Preemptive'),
('PREEMPTIVE_SNIOPEN','Preemptive'),
('PREEMPTIVE_SOSHOST','Preemptive'),
('PREEMPTIVE_SOSTESTING','Preemptive'),
('PREEMPTIVE_SP_SERVER_DIAGNOSTICS','Preemptive'),
('PREEMPTIVE_STARTRM','Preemptive'),
('PREEMPTIVE_STREAMFCB_CHECKPOINT','Preemptive'),
('PREEMPTIVE_STREAMFCB_RECOVER','Preemptive'),
('PREEMPTIVE_STRESSDRIVER','Preemptive'),
('PREEMPTIVE_TESTING','Preemptive'),
('PREEMPTIVE_TRANSIMPORT','Preemptive'),
('PREEMPTIVE_UNMARSHALPROPAGATIONTOKEN','Preemptive'),
('PREEMPTIVE_VSS_CREATESNAPSHOT','Preemptive'),
('PREEMPTIVE_VSS_CREATEVOLUMESNAPSHOT','Preemptive'),
('PREEMPTIVE_XE_CALLBACKEXECUTE','Preemptive'),
('PREEMPTIVE_XE_CX_FILE_OPEN','Preemptive'),
('PREEMPTIVE_XE_CX_HTTP_CALL','Preemptive'),
('PREEMPTIVE_XE_DISPATCHER','Preemptive'),
('PREEMPTIVE_XE_ENGINEINIT','Preemptive'),
('PREEMPTIVE_XE_GETTARGETSTATE','Preemptive'),
('PREEMPTIVE_XE_SESSIONCOMMIT','Preemptive'),
('PREEMPTIVE_XE_TARGETFINALIZE','Preemptive'),
('PREEMPTIVE_XE_TARGETINIT','Preemptive'),
('PREEMPTIVE_XE_TIMERRUN','Preemptive'),
('PREEMPTIVE_XETESTING','Preemptive'),
('PWAIT_HADR_ACTION_COMPLETED','Replication'),
('PWAIT_HADR_CHANGE_NOTIFIER_TERMINATION_SYNC','Replication'),
('PWAIT_HADR_CLUSTER_INTEGRATION','Replication'),
('PWAIT_HADR_FAILOVER_COMPLETED','Replication'),
('PWAIT_HADR_JOIN','Replication'),
('PWAIT_HADR_OFFLINE_COMPLETED','Replication'),
('PWAIT_HADR_ONLINE_COMPLETED','Replication'),
('PWAIT_HADR_POST_ONLINE_COMPLETED','Replication'),
('PWAIT_HADR_SERVER_READY_CONNECTIONS','Replication'),
('PWAIT_HADR_WORKITEM_COMPLETED','Replication'),
('PWAIT_HADRSIM','Replication'),
('PWAIT_RESOURCE_SEMAPHORE_FT_PARALLEL_QUERY_SYNC','Full Text Search'),
('QUERY_TRACEOUT','Tracing'),
('REPL_CACHE_ACCESS','Replication'),
('REPL_HISTORYCACHE_ACCESS','Replication'),
('REPL_SCHEMA_ACCESS','Replication'),
('REPL_TRANFSINFO_ACCESS','Replication'),
('REPL_TRANHASHTABLE_ACCESS','Replication'),
('REPL_TRANTEXTINFO_ACCESS','Replication'),
('REPLICA_WRITES','Replication'),
('REQUEST_FOR_DEADLOCK_SEARCH','Idle'),
('RESERVED_MEMORY_ALLOCATION_EXT','Memory'),
('RESOURCE_SEMAPHORE','Memory'),
('RESOURCE_SEMAPHORE_QUERY_COMPILE','Compilation'),
('SLEEP_BPOOL_FLUSH','Idle'),
('SLEEP_BUFFERPOOL_HELPLW','Idle'),
('SLEEP_DBSTARTUP','Idle'),
('SLEEP_DCOMSTARTUP','Idle'),
('SLEEP_MASTERDBREADY','Idle'),
('SLEEP_MASTERMDREADY','Idle'),
('SLEEP_MASTERUPGRADED','Idle'),
('SLEEP_MEMORYPOOL_ALLOCATEPAGES','Idle'),
('SLEEP_MSDBSTARTUP','Idle'),
('SLEEP_RETRY_VIRTUALALLOC','Idle'),
('SLEEP_SYSTEMTASK','Idle'),
('SLEEP_TASK','Idle'),
('SLEEP_TEMPDBSTARTUP','Idle'),
('SLEEP_WORKSPACE_ALLOCATEPAGE','Idle'),
('SOS_SCHEDULER_YIELD','CPU'),
('SQLCLR_APPDOMAIN','SQL CLR'),
('SQLCLR_ASSEMBLY','SQL CLR'),
('SQLCLR_DEADLOCK_DETECTION','SQL CLR'),
('SQLCLR_QUANTUM_PUNISHMENT','SQL CLR'),
('SQLTRACE_BUFFER_FLUSH','Idle'),
('SQLTRACE_FILE_BUFFER','Tracing'),
('SQLTRACE_FILE_READ_IO_COMPLETION','Tracing'),
('SQLTRACE_FILE_WRITE_IO_COMPLETION','Tracing'),
('SQLTRACE_INCREMENTAL_FLUSH_SLEEP','Idle'),
('SQLTRACE_PENDING_BUFFER_WRITERS','Tracing'),
('SQLTRACE_SHUTDOWN','Tracing'),
('SQLTRACE_WAIT_ENTRIES','Idle'),
('THREADPOOL','Worker Thread'),
('TRACE_EVTNOTIF','Tracing'),
('TRACEWRITE','Tracing'),
('TRAN_MARKLATCH_DT','Transaction'),
('TRAN_MARKLATCH_EX','Transaction'),
('TRAN_MARKLATCH_KP','Transaction'),
('TRAN_MARKLATCH_NL','Transaction'),
('TRAN_MARKLATCH_SH','Transaction'),
('TRAN_MARKLATCH_UP','Transaction'),
('TRANSACTION_MUTEX','Transaction'),
('WAIT_FOR_RESULTS','User Wait'),
('WAITFOR','User Wait'),
('WRITE_COMPLETION','Other Disk IO'),
('WRITELOG','Tran Log IO'),
('XACT_OWN_TRANSACTION','Transaction'),
('XACT_RECLAIM_SESSION','Transaction'),
('XACTLOCKINFO','Transaction'),
('XACTWORKSPACE_MUTEX','Transaction'),
('XE_DISPATCHER_WAIT','Idle'),
('XE_TIMER_EVENT','Idle')) AS wc(wait_type, wait_category)
	ON ws.wait_type = wc.wait_type
WHERE
ws.wait_type NOT IN (
	N'BROKER_EVENTHANDLER', N'BROKER_RECEIVE_WAITFOR', N'BROKER_TASK_STOP',
	N'BROKER_TO_FLUSH', N'BROKER_TRANSMITTER', N'CHECKPOINT_QUEUE',
	N'CHKPT', N'CLR_AUTO_EVENT', N'CLR_MANUAL_EVENT', N'CLR_SEMAPHORE',
	N'DBMIRROR_DBM_EVENT', N'DBMIRROR_EVENTS_QUEUE', N'DBMIRROR_WORKER_QUEUE',
	N'DBMIRRORING_CMD', N'DIRTY_PAGE_POLL', N'DISPATCHER_QUEUE_SEMAPHORE',
	N'EXECSYNC', N'FSAGENT', N'FT_IFTS_SCHEDULER_IDLE_WAIT', N'FT_IFTSHC_MUTEX',
	N'HADR_CLUSAPI_CALL', N'HADR_FILESTREAM_IOMGR_IOCOMPLETION', N'HADR_LOGCAPTURE_WAIT',
	N'HADR_NOTIFICATION_DEQUEUE', N'HADR_TIMER_TASK', N'HADR_WORK_QUEUE',
	N'KSOURCE_WAKEUP', N'LAZYWRITER_SLEEP', N'LOGMGR_QUEUE',
	N'MEMORY_ALLOCATION_EXT', N'ONDEMAND_TASK_QUEUE',
	N'PARALLEL_REDO_WORKER_WAIT_WORK',
	N'PREEMPTIVE_HADR_LEASE_MECHANISM', N'PREEMPTIVE_SP_SERVER_DIAGNOSTICS',
	N'PREEMPTIVE_OS_LIBRARYOPS', N'PREEMPTIVE_OS_COMOPS', N'PREEMPTIVE_OS_CRYPTOPS',
	N'PREEMPTIVE_OS_PIPEOPS','PREEMPTIVE_OS_GENERICOPS', N'PREEMPTIVE_OS_VERIFYTRUST',
	N'PREEMPTIVE_OS_DEVICEOPS',
	N'PREEMPTIVE_XE_CALLBACKEXECUTE', N'PREEMPTIVE_XE_DISPATCHER',
	N'PREEMPTIVE_XE_GETTARGETSTATE', N'PREEMPTIVE_XE_SESSIONCOMMIT',
	N'PREEMPTIVE_XE_TARGETINIT', N'PREEMPTIVE_XE_TARGETFINALIZE',
	N'PWAIT_ALL_COMPONENTS_INITIALIZED', N'PWAIT_DIRECTLOGCONSUMER_GETNEXT',
	N'QDS_PERSIST_TASK_MAIN_LOOP_SLEEP',
	N'QDS_ASYNC_QUEUE',
	N'QDS_CLEANUP_STALE_QUERIES_TASK_MAIN_LOOP_SLEEP', N'REQUEST_FOR_DEADLOCK_SEARCH',
	N'RESOURCE_QUEUE', N'SERVER_IDLE_CHECK', N'SLEEP_BPOOL_FLUSH', N'SLEEP_DBSTARTUP',
	N'SLEEP_DCOMSTARTUP', N'SLEEP_MASTERDBREADY', N'SLEEP_MASTERMDREADY',
	N'SLEEP_MASTERUPGRADED', N'SLEEP_MSDBSTARTUP', N'SLEEP_SYSTEMTASK', N'SLEEP_TASK',
	N'SLEEP_TEMPDBSTARTUP', N'SNI_HTTP_ACCEPT', N'SP_SERVER_DIAGNOSTICS_SLEEP',
	N'SQLTRACE_BUFFER_FLUSH', N'SQLTRACE_INCREMENTAL_FLUSH_SLEEP',
	N'SQLTRACE_WAIT_ENTRIES',
	N'WAIT_FOR_RESULTS', N'WAITFOR', N'WAITFOR_TASKSHUTDOWN', N'WAIT_XTP_HOST_WAIT',
	N'WAIT_XTP_OFFLINE_CKPT_NEW_LOG', N'WAIT_XTP_CKPT_CLOSE',
	N'XE_BUFFERMGR_ALLPROCESSED_EVENT', N'XE_DISPATCHER_JOIN',
	N'XE_DISPATCHER_WAIT', N'XE_LIVE_TARGET_TVF', N'XE_TIMER_EVENT',
	N'SOS_WORK_DISPATCHER','RESERVED_MEMORY_ALLOCATION_EXT')
AND waiting_tasks_count > 0
AND wait_time_ms > 100;

ELSE
	SELECT
	'sqlserver_azuredb_waitstats' AS [measurement],
	REPLACE(@@SERVERNAME,'\',':') AS [sql_instance],
	DB_NAME() as [database_name'],
	dbws.wait_type,
	dbws.wait_time_ms,
	dbws.wait_time_ms - signal_wait_time_ms AS [resource_wait_ms],
	dbws.signal_wait_time_ms,
	dbws.max_wait_time_ms,
	dbws.waiting_tasks_count
	FROM
	sys.dm_db_wait_stats AS dbws WITH (NOLOCK)
	WHERE
		dbws.wait_type NOT IN (
		N'BROKER_EVENTHANDLER', N'BROKER_RECEIVE_WAITFOR', N'BROKER_TASK_STOP',
		N'BROKER_TO_FLUSH', N'BROKER_TRANSMITTER', N'CHECKPOINT_QUEUE',
		N'CHKPT', N'CLR_AUTO_EVENT', N'CLR_MANUAL_EVENT', N'CLR_SEMAPHORE',
		N'DBMIRROR_DBM_EVENT', N'DBMIRROR_EVENTS_QUEUE', N'DBMIRROR_WORKER_QUEUE',
		N'DBMIRRORING_CMD', N'DIRTY_PAGE_POLL', N'DISPATCHER_QUEUE_SEMAPHORE',
		N'EXECSYNC', N'FSAGENT', N'FT_IFTS_SCHEDULER_IDLE_WAIT', N'FT_IFTSHC_MUTEX',
		N'HADR_CLUSAPI_CALL', N'HADR_FILESTREAM_IOMGR_IOCOMPLETION', N'HADR_LOGCAPTURE_WAIT',
		N'HADR_NOTIFICATION_DEQUEUE', N'HADR_TIMER_TASK', N'HADR_WORK_QUEUE',
		N'KSOURCE_WAKEUP', N'LAZYWRITER_SLEEP', N'LOGMGR_QUEUE',
		N'MEMORY_ALLOCATION_EXT', N'ONDEMAND_TASK_QUEUE',
		N'PARALLEL_REDO_WORKER_WAIT_WORK',
		N'PREEMPTIVE_HADR_LEASE_MECHANISM', N'PREEMPTIVE_SP_SERVER_DIAGNOSTICS',
		N'PREEMPTIVE_OS_LIBRARYOPS', N'PREEMPTIVE_OS_COMOPS', N'PREEMPTIVE_OS_CRYPTOPS',
		N'PREEMPTIVE_OS_PIPEOPS','PREEMPTIVE_OS_GENERICOPS', N'PREEMPTIVE_OS_VERIFYTRUST',
		N'PREEMPTIVE_OS_DEVICEOPS',
		N'PREEMPTIVE_XE_CALLBACKEXECUTE', N'PREEMPTIVE_XE_DISPATCHER',
		N'PREEMPTIVE_XE_GETTARGETSTATE', N'PREEMPTIVE_XE_SESSIONCOMMIT',
		N'PREEMPTIVE_XE_TARGETINIT', N'PREEMPTIVE_XE_TARGETFINALIZE',
		N'PWAIT_ALL_COMPONENTS_INITIALIZED', N'PWAIT_DIRECTLOGCONSUMER_GETNEXT',
		N'QDS_PERSIST_TASK_MAIN_LOOP_SLEEP',
		N'QDS_ASYNC_QUEUE',
		N'QDS_CLEANUP_STALE_QUERIES_TASK_MAIN_LOOP_SLEEP', N'REQUEST_FOR_DEADLOCK_SEARCH',
		N'RESOURCE_QUEUE', N'SERVER_IDLE_CHECK', N'SLEEP_BPOOL_FLUSH', N'SLEEP_DBSTARTUP',
		N'SLEEP_DCOMSTARTUP', N'SLEEP_MASTERDBREADY', N'SLEEP_MASTERMDREADY',
		N'SLEEP_MASTERUPGRADED', N'SLEEP_MSDBSTARTUP', N'SLEEP_SYSTEMTASK', N'SLEEP_TASK',
		N'SLEEP_TEMPDBSTARTUP', N'SNI_HTTP_ACCEPT', N'SP_SERVER_DIAGNOSTICS_SLEEP',
		N'SQLTRACE_BUFFER_FLUSH', N'SQLTRACE_INCREMENTAL_FLUSH_SLEEP',
		N'SQLTRACE_WAIT_ENTRIES',
		N'WAIT_FOR_RESULTS', N'WAITFOR', N'WAITFOR_TASKSHUTDOWN', N'WAIT_XTP_HOST_WAIT',
		N'WAIT_XTP_OFFLINE_CKPT_NEW_LOG', N'WAIT_XTP_CKPT_CLOSE',
		N'XE_BUFFERMGR_ALLPROCESSED_EVENT', N'XE_DISPATCHER_JOIN',
		N'XE_DISPATCHER_WAIT', N'XE_LIVE_TARGET_TVF', N'XE_TIMER_EVENT',
		N'SOS_WORK_DISPATCHER','RESERVED_MEMORY_ALLOCATION_EXT')
	AND waiting_tasks_count > 0
	AND wait_time_ms > 100;
`

// Only executed if AzureDB flag is set
const sqlAzureDBResourceStats string = `SET DEADLOCK_PRIORITY -10;
IF SERVERPROPERTY('EngineEdition') = 5  -- Is this Azure SQL DB?
BEGIN
        SELECT TOP(1)
                'sqlserver_azure_db_resource_stats' AS [measurement],
                REPLACE(@@SERVERNAME,'\',':') AS [sql_instance],
                DB_NAME() as [database_name],
                cast(avg_cpu_percent as float) as avg_cpu_percent,
                cast(avg_data_io_percent as float) as avg_data_io_percent,
                cast(avg_log_write_percent as float) as avg_log_write_percent,
                cast(avg_memory_usage_percent as float) as avg_memory_usage_percent,
                cast(xtp_storage_percent as float) as xtp_storage_percent,
                cast(max_worker_percent as float) as max_worker_percent,
                cast(max_session_percent as float) as max_session_percent,
                dtu_limit,
                cast(avg_login_rate_percent as float) as avg_login_rate_percent  ,
                end_time,
                cast(avg_instance_memory_percent as float) as avg_instance_memory_percent ,
                cast(avg_instance_cpu_percent as float) as avg_instance_cpu_percent
        FROM
                sys.dm_db_resource_stats WITH (NOLOCK)
        ORDER BY
                end_time DESC
END
`

//Only executed if AzureDB Flag is set
const sqlAzureDBResourceGovernance string = `
IF SERVERPROPERTY('EngineEdition') = 5  -- Is this Azure SQL DB?
SELECT
  'sqlserver_db_resource_governance' AS [measurement],
   REPLACE(@@SERVERNAME,'\',':') AS [sql_instance],
   DB_NAME() as [database_name],
   slo_name,
	dtu_limit,
	max_cpu,
	cap_cpu,
	instance_cap_cpu,
	max_db_memory,
	max_db_max_size_in_mb,
	db_file_growth_in_mb,
	log_size_in_mb,
	instance_max_worker_threads,
	primary_group_max_workers,
	instance_max_log_rate,
	primary_min_log_rate,
	primary_max_log_rate,
	primary_group_min_io,
	primary_group_max_io,
	primary_group_min_cpu,
	primary_group_max_cpu,
	primary_pool_max_workers,
	pool_max_io,
	checkpoint_rate_mbps,
	checkpoint_rate_io,
	volume_local_iops,
	volume_managed_xstore_iops,
	volume_external_xstore_iops,
	volume_type_local_iops,
	volume_type_managed_xstore_iops,
	volume_type_external_xstore_iops,
	volume_pfs_iops,
	volume_type_pfs_iops
    FROM
    sys.dm_user_db_resource_governance WITH (NOLOCK);
ELSE
BEGIN
        IF SERVERPROPERTY('EngineEdition') = 8  -- Is this Azure SQL Managed Instance?
         SELECT
           'sqlserver_instance_resource_governance' AS [measurement],
           REPLACE(@@SERVERNAME,'\',':') AS [sql_instance],
           instance_cap_cpu,
           instance_max_log_rate,
           instance_max_worker_threads,
           tempdb_log_file_number,
           volume_local_iops,
           volume_external_xstore_iops,
           volume_managed_xstore_iops,
           volume_type_local_iops as voltype_local_iops,
           volume_type_managed_xstore_iops as voltype_man_xtore_iops,
           volume_type_external_xstore_iops as voltype_ext_xtore_iops,
           volume_external_xstore_iops  as vol_ext_xtore_iops
           from
            sys.dm_instance_resource_governance
  END;
`

const sqlServerRequestsV2 string = `
SET NOCOUNT ON;
IF(OBJECT_ID('tempdb..#blockingSessions') IS NOT NULL) DROP TABLE #blockingSessions;
SELECT  blocking_session_id into #blockingSessions FROM sys.dm_exec_requests WHERE blocking_session_id != 0
create index ix_blockingSessions_1 on #blockingSessions (blocking_session_id)
SELECT	
   'sqlserver_requests' AS [measurement],
    REPLACE(@@SERVERNAME,'\',':') AS [sql_instance],
    DB_NAME(r.database_id) as [database],
	r.session_id
	, r.request_id
	, r.status
	, r.cpu_time as cpu_time_ms
	, r.total_elapsed_time as total_elapsed_time_ms
	, r.logical_reads
	, r.writes
	, r.command
	, wait_time as wait_time_ms
	, wait_type
	, wait_resource
	, blocking_session_id
	, s.program_name
	, s.host_name
	, s.nt_user_name
	 , r.open_transaction_count  AS open_transaction
	 , 	LEFT (CASE COALESCE(r.transaction_isolation_level, s.transaction_isolation_level)
		WHEN 0 THEN '0-Read Committed' 
		WHEN 1 THEN '1-Read Uncommitted (NOLOCK)' 
		WHEN 2 THEN '2-Read Committed' 
		WHEN 3 THEN '3-Repeatable Read' 
		WHEN 4 THEN '4-Serializable' 
		WHEN 5 THEN '5-Snapshot' 
		ELSE CONVERT (varchar(30), r.transaction_isolation_level) + '-UNKNOWN' 
	END, 30) AS transaction_isolation_level
	,r.granted_query_memory as granted_query_memory_pages
	, r.percent_complete
	, (SUBSTRING(qt.text, r.statement_start_offset / 2 + 1,
		(CASE WHEN r.statement_end_offset = -1
				THEN LEN(CONVERT(NVARCHAR(MAX), qt.text)) * 2
				ELSE r.statement_end_offset
		END - r.statement_start_offset) / 2)
) AS statement_text
	, qt.objectid
	, QUOTENAME(OBJECT_SCHEMA_NAME(qt.objectid,qt.dbid)) + '.' +  QUOTENAME(OBJECT_NAME(qt.objectid,qt.dbid)) as stmt_object_name
	, DB_NAME(qt.dbid) stmt_db_name
	,CONVERT(varchar(20),[query_hash],1) as [query_hash]
	,CONVERT(varchar(20),[query_plan_hash],1) as [query_plan_hash]
	FROM	sys.dm_exec_requests r WITH (NOLOCK)
		LEFT OUTER JOIN sys.dm_exec_sessions s WITH (NOLOCK) ON (s.session_id = r.session_id)
		OUTER APPLY sys.dm_exec_sql_text(sql_handle) AS qt
		
	WHERE ((r.session_id IS NOT NULL AND (s.is_user_process = 1 OR r.status COLLATE Latin1_General_BIN NOT IN ('background', 'sleeping')))
		OR (s.session_id IN (SELECT blocking_session_id FROM #blockingSessions)))
		AND blocking_session_id > 0
	 OPTION(MAXDOP 1)

`

const sqlServerVolumeSpaceV2 string = `
/* Only for on-prem version of SQL Server
Gets data about disk space, only for volumes used by SQL Server (data available form sql 2008R2 and later)
*/
DECLARE
	 @EngineEdition AS tinyint = CAST(SERVERPROPERTY('EngineEdition') AS int)
	,@MajorMinorVersion AS int = CAST(PARSENAME(CAST(SERVERPROPERTY('ProductVersion') as nvarchar),4) AS int)*100 + CAST(PARSENAME(CAST(SERVERPROPERTY('ProductVersion') as nvarchar),3) AS int)
	
IF @EngineEdition IN (2,3,4) AND @MajorMinorVersion >= 1050
	BEGIN
	SELECT DISTINCT
		'sqlserver_volume_space' AS [measurement]
		,SERVERPROPERTY('machinename') AS [server_name]
		,REPLACE(@@SERVERNAME,'\',':') AS [sql_instance]
		/*in [volume_mount_point] any trailing "\" char will be removed by telegraf */
		,[volume_mount_point]
		,vs.[total_bytes] AS [total_space_bytes]
		,vs.[available_bytes] AS [available_space_bytes]
		,vs.[total_bytes] - vs.[available_bytes] AS [used_space_bytes]
	FROM
		sys.master_files as mf
		CROSS APPLY sys.dm_os_volume_stats(mf.database_id, mf.file_id) as vs
	END
`

const sqlServerCpuV2 string = `
/*The ring buffer has a new value every minute*/
IF SERVERPROPERTY('EngineEdition') IN (2,3,4) /*Standard,Enterpris,Express*/
BEGIN
SELECT 
	 'sqlserver_cpu' AS [measurement]
	,REPLACE(@@SERVERNAME,'\',':') AS [sql_instance]
	,[SQLProcessUtilization] AS [sqlserver_process_cpu]
	,[SystemIdle] AS [system_idle_cpu]
	,100 - [SystemIdle] - [SQLProcessUtilization] AS [other_process_cpu]
FROM (
	SELECT TOP 1
		 [record_id]
		/*,dateadd(ms, (y.[timestamp] - (SELECT CAST([ms_ticks] AS BIGINT) FROM sys.dm_os_sys_info)), GETDATE()) AS [EventTime] --use for check/debug purpose*/
		,[SQLProcessUtilization]
		,[SystemIdle]
	FROM (
		SELECT record.value('(./Record/@id)[1]', 'int') AS [record_id]
			,record.value('(./Record/SchedulerMonitorEvent/SystemHealth/SystemIdle)[1]', 'int') AS [SystemIdle]
			,record.value('(./Record/SchedulerMonitorEvent/SystemHealth/ProcessUtilization)[1]', 'int') AS [SQLProcessUtilization]
			,[TIMESTAMP]
		FROM (
			SELECT [TIMESTAMP]
				,convert(XML, [record]) AS [record]
			FROM sys.dm_os_ring_buffers
			WHERE [ring_buffer_type] = N'RING_BUFFER_SCHEDULER_MONITOR'
				AND [record] LIKE '%<SystemHealth>%'
			) AS x
		) AS y
	ORDER BY record_id DESC
) as z

END
`

// Queries V1
const sqlPerformanceMetrics string = `SET DEADLOCK_PRIORITY -10;
SET NOCOUNT ON;
SET ARITHABORT ON;
SET QUOTED_IDENTIFIER ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED

DECLARE @PCounters TABLE
(
	counter_name nvarchar(64),
	cntr_value bigint,
	Primary Key(counter_name)
);

INSERT @PCounters (counter_name, cntr_value)
SELECT 'Point In Time Recovery', Value = CASE
	WHEN  1 > 1.0 * COUNT(*)  / NULLIF((SELECT COUNT(*) FROM sys.databases d WHERE database_id > 4), 0)
	THEN 0 ELSE 1 END
FROM sys.databases d
WHERE database_id > 4
	AND recovery_model IN (1)
UNION ALL
SELECT 'Page File Usage (%)', CAST(100 * (1 - available_page_file_kb * 1. / total_page_file_kb) as decimal(9,2)) as [PageFileUsage (%)]
FROM sys.dm_os_sys_memory
UNION ALL
SELECT 'Connection memory per connection (bytes)',  Ratio = CAST((cntr_value / (SELECT 1.0 * cntr_value FROM sys.dm_os_performance_counters WHERE counter_name = 'User Connections')) * 1024 as int)
FROM sys.dm_os_performance_counters
WHERE counter_name = 'Connection Memory (KB)'
UNION ALL
SELECT 'Available physical memory (bytes)', available_physical_memory_kb * 1024
FROM sys.dm_os_sys_memory
UNION ALL
SELECT 'Signal wait (%)', SignalWaitPercent = CAST(100.0 * SUM(signal_wait_time_ms) / SUM (wait_time_ms) AS NUMERIC(20,2))
FROM sys.dm_os_wait_stats
UNION ALL
SELECT 'Sql compilation per batch request',  SqlCompilationPercent = 100.0 * cntr_value / (SELECT 1.0*cntr_value FROM sys.dm_os_performance_counters WHERE counter_name = 'Batch Requests/sec')
FROM sys.dm_os_performance_counters
WHERE counter_name = 'SQL Compilations/sec'
UNION ALL
SELECT 'Sql recompilation per batch request', SqlReCompilationPercent = 100.0 *cntr_value / (SELECT 1.0*cntr_value FROM sys.dm_os_performance_counters WHERE counter_name = 'Batch Requests/sec')
FROM sys.dm_os_performance_counters
WHERE counter_name = 'SQL Re-Compilations/sec'
UNION ALL
SELECT 'Page lookup per batch request',PageLookupPercent = 100.0 * cntr_value / (SELECT 1.0*cntr_value FROM sys.dm_os_performance_counters WHERE counter_name = 'Batch Requests/sec')
FROM sys.dm_os_performance_counters
WHERE counter_name = 'Page lookups/sec'
UNION ALL
SELECT 'Page split per batch request',PageSplitPercent = 100.0 * cntr_value / (SELECT 1.0*cntr_value FROM sys.dm_os_performance_counters WHERE counter_name = 'Batch Requests/sec')
FROM sys.dm_os_performance_counters
WHERE counter_name = 'Page splits/sec'
UNION ALL
SELECT 'Average tasks', AverageTaskCount = (SELECT AVG(current_tasks_count) FROM sys.dm_os_schedulers WITH (NOLOCK) WHERE scheduler_id < 255 )
UNION ALL
SELECT 'Average runnable tasks', AverageRunnableTaskCount = (SELECT AVG(runnable_tasks_count) FROM sys.dm_os_schedulers WITH (NOLOCK) WHERE scheduler_id < 255 )
UNION ALL
SELECT 'Average pending disk IO', AveragePendingDiskIOCount = (SELECT AVG(pending_disk_io_count) FROM sys.dm_os_schedulers WITH (NOLOCK) WHERE scheduler_id < 255 )
UNION ALL
SELECT 'Buffer pool rate (bytes/sec)', BufferPoolRate = (1.0*cntr_value * 8 * 1024) /
	(SELECT 1.0*cntr_value FROM sys.dm_os_performance_counters  WHERE object_name like '%Buffer Manager%' AND counter_name = 'Page life expectancy')
FROM sys.dm_os_performance_counters
WHERE object_name like '%Buffer Manager%'
AND counter_name = 'Database pages'
UNION ALL
SELECT 'Memory grant pending', MemoryGrantPending = cntr_value
FROM sys.dm_os_performance_counters
WHERE counter_name = 'Memory Grants Pending'
UNION ALL
SELECT 'Readahead per page read', Readahead = 100.0 *cntr_value / (SELECT 1.0*cntr_value FROM sys.dm_os_performance_counters WHERE counter_name = 'Page Reads/sec')
FROM sys.dm_os_performance_counters
WHERE counter_name = 'Readahead pages/sec'
UNION ALL
SELECT 'Total target memory ratio', TotalTargetMemoryRatio = 100.0 * cntr_value / (SELECT 1.0*cntr_value FROM sys.dm_os_performance_counters WHERE counter_name = 'Target Server Memory (KB)')
FROM sys.dm_os_performance_counters
WHERE counter_name = 'Total Server Memory (KB)'

IF OBJECT_ID('tempdb..#PCounters') IS NOT NULL DROP TABLE #PCounters;
SELECT * INTO #PCounters FROM @PCounters

DECLARE @DynamicPivotQuery AS NVARCHAR(MAX)
DECLARE @ColumnName AS NVARCHAR(MAX)
SELECT @ColumnName= ISNULL(@ColumnName + ',','') + QUOTENAME(counter_name)
FROM (SELECT DISTINCT counter_name FROM @PCounters) AS bl

SET @DynamicPivotQuery = N'
SELECT measurement = ''Performance metrics'', servername = REPLACE(@@SERVERNAME, ''\'', '':''), type = ''Performance metrics''
, ' + @ColumnName + '  FROM
(
SELECT counter_name, cntr_value
FROM #PCounters
) as V
PIVOT(SUM(cntr_value) FOR counter_name IN (' + @ColumnName + ')) AS PVTTable
'
EXEC sp_executesql @DynamicPivotQuery;
`

const sqlMemoryClerk string = `SET DEADLOCK_PRIORITY -10;
SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

DECLARE @sqlVers numeric(4,2)
SELECT @sqlVers = LEFT(CAST(SERVERPROPERTY('productversion') as varchar), 4)

IF OBJECT_ID('tempdb..#clerk') IS NOT NULL
	DROP TABLE #clerk;

CREATE TABLE #clerk (
    ClerkCategory nvarchar(64) NOT NULL,
    UsedPercent decimal(9,2),
    UsedBytes bigint
);

DECLARE @DynamicClerkQuery AS NVARCHAR(MAX)

IF @sqlVers < 11
BEGIN
    SET @DynamicClerkQuery = N'
    INSERT #clerk (ClerkCategory, UsedPercent, UsedBytes)
    SELECT ClerkCategory
    , UsedPercent = SUM(UsedPercent)
    , UsedBytes = SUM(UsedBytes)
    FROM
    (
    SELECT ClerkCategory = CASE MC.[type]
        WHEN ''MEMORYCLERK_SQLBUFFERPOOL'' THEN ''Buffer pool''
        WHEN ''CACHESTORE_SQLCP'' THEN ''Cache (sql plans)''
        WHEN ''CACHESTORE_OBJCP'' THEN ''Cache (objects)''
        ELSE ''Other'' END
    , SUM((single_pages_kb + multi_pages_kb) * 1024) AS UsedBytes
    , Cast(100 * Sum((single_pages_kb + multi_pages_kb))*1.0/(Select Sum((single_pages_kb + multi_pages_kb)) From sys.dm_os_memory_clerks) as Decimal(7, 4)) UsedPercent
    FROM sys.dm_os_memory_clerks MC
    WHERE (single_pages_kb + multi_pages_kb) > 0
    GROUP BY CASE MC.[type]
        WHEN ''MEMORYCLERK_SQLBUFFERPOOL'' THEN ''Buffer pool''
        WHEN ''CACHESTORE_SQLCP'' THEN ''Cache (sql plans)''
        WHEN ''CACHESTORE_OBJCP'' THEN ''Cache (objects)''
        ELSE ''Other'' END
    ) as T
    GROUP BY ClerkCategory;
    '
END
ELSE
BEGIN
    SET @DynamicClerkQuery = N'
    INSERT #clerk (ClerkCategory, UsedPercent, UsedBytes)
    SELECT ClerkCategory
    , UsedPercent = SUM(UsedPercent)
    , UsedBytes = SUM(UsedBytes)
    FROM
    (
    SELECT ClerkCategory = CASE MC.[type]
        WHEN ''MEMORYCLERK_SQLBUFFERPOOL'' THEN ''Buffer pool''
        WHEN ''CACHESTORE_SQLCP'' THEN ''Cache (sql plans)''
        WHEN ''CACHESTORE_OBJCP'' THEN ''Cache (objects)''
        ELSE ''Other'' END
    , SUM(pages_kb * 1024) AS UsedBytes
    , Cast(100 * Sum(pages_kb)*1.0/(Select Sum(pages_kb) From sys.dm_os_memory_clerks) as Decimal(7, 4)) UsedPercent
    FROM sys.dm_os_memory_clerks MC
    WHERE pages_kb > 0
    GROUP BY CASE MC.[type]
        WHEN ''MEMORYCLERK_SQLBUFFERPOOL'' THEN ''Buffer pool''
        WHEN ''CACHESTORE_SQLCP'' THEN ''Cache (sql plans)''
        WHEN ''CACHESTORE_OBJCP'' THEN ''Cache (objects)''
        ELSE ''Other'' END
    ) as T
    GROUP BY ClerkCategory;
    '
END
EXEC sp_executesql @DynamicClerkQuery;
SELECT
-- measurement
measurement
-- tags
, servername= REPLACE(@@SERVERNAME, '\', ':')
, type = 'Memory clerk'
-- value
, [Buffer pool]
, [Cache (objects)]
, [Cache (sql plans)]
, [Other]
FROM
(
SELECT measurement = 'Memory breakdown (%)'
, [Buffer pool] = ISNULL(ROUND([Buffer Pool], 1), 0)
, [Cache (objects)] = ISNULL(ROUND([Cache (objects)], 1), 0)
, [Cache (sql plans)] = ISNULL(ROUND([Cache (sql plans)], 1), 0)
, [Other] = ISNULL(ROUND([Other], 1), 0)
FROM (SELECT ClerkCategory, UsedPercent FROM #clerk) as G1
PIVOT
(
	SUM(UsedPercent)
	FOR ClerkCategory IN ([Buffer Pool], [Cache (objects)], [Cache (sql plans)], [Other])
) AS PivotTable

UNION ALL

SELECT measurement = 'Memory breakdown (bytes)'
, [Buffer pool] = ISNULL(ROUND([Buffer Pool], 1), 0)
, [Cache (objects)] = ISNULL(ROUND([Cache (objects)], 1), 0)
, [Cache (sql plans)] = ISNULL(ROUND([Cache (sql plans)], 1), 0)
, [Other] = ISNULL(ROUND([Other], 1), 0)
FROM (SELECT ClerkCategory, UsedBytes FROM #clerk) as G2
PIVOT
(
	SUM(UsedBytes)
	FOR ClerkCategory IN ([Buffer Pool], [Cache (objects)], [Cache (sql plans)], [Other])
) AS PivotTable
) as T;
`

const sqlDatabaseSize string = `SET DEADLOCK_PRIORITY -10;
SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED

IF OBJECT_ID('tempdb..#baseline') IS NOT NULL
	DROP TABLE #baseline;
SELECT
    DB_NAME(mf.database_id) AS database_name ,
    CAST(mf.size AS BIGINT) as database_size_8k_pages,
    CAST(mf.max_size AS BIGINT) as database_max_size_8k_pages,
    size_on_disk_bytes ,
	type_desc as datafile_type,
    GETDATE() AS baselineDate
INTO #baseline
FROM sys.dm_io_virtual_file_stats(NULL, NULL) AS divfs
INNER JOIN sys.master_files AS mf ON mf.database_id = divfs.database_id
	AND mf.file_id = divfs.file_id

DECLARE @DynamicPivotQuery AS NVARCHAR(MAX)
DECLARE @ColumnName AS NVARCHAR(MAX), @ColumnName2 AS NVARCHAR(MAX)

SELECT @ColumnName= ISNULL(@ColumnName + ',','') + QUOTENAME(database_name)
FROM (SELECT DISTINCT database_name FROM #baseline) AS bl

--Prepare the PIVOT query using the dynamic
SET @DynamicPivotQuery = N'
SELECT measurement = ''Log size (bytes)'', servername = REPLACE(@@SERVERNAME, ''\'', '':''), type = ''Database size''
, ' + @ColumnName + '  FROM
(
SELECT database_name, size_on_disk_bytes
FROM #baseline
WHERE datafile_type = ''LOG''
) as V
PIVOT(SUM(size_on_disk_bytes) FOR database_name IN (' + @ColumnName + ')) AS PVTTable

UNION ALL

SELECT measurement = ''Rows size (bytes)'', servername = REPLACE(@@SERVERNAME, ''\'', '':''), type = ''Database size''
, ' + @ColumnName + '  FROM
(
SELECT database_name, size_on_disk_bytes
FROM #baseline
WHERE datafile_type = ''ROWS''
) as V
PIVOT(SUM(size_on_disk_bytes) FOR database_name IN (' + @ColumnName + ')) AS PVTTable

UNION ALL

SELECT measurement = ''Rows size (8KB pages)'', servername = REPLACE(@@SERVERNAME, ''\'', '':''), type = ''Database size''
, ' + @ColumnName + '  FROM
(
SELECT database_name, database_size_8k_pages
FROM #baseline
WHERE datafile_type = ''ROWS''
) as V
PIVOT(SUM(database_size_8k_pages) FOR database_name IN (' + @ColumnName + ')) AS PVTTable

UNION ALL

SELECT measurement = ''Log size (8KB pages)'', servername = REPLACE(@@SERVERNAME, ''\'', '':''), type = ''Database size''
, ' + @ColumnName + '  FROM
(
SELECT database_name, database_size_8k_pages
FROM #baseline
WHERE datafile_type = ''LOG''
) as V
PIVOT(SUM(database_size_8k_pages) FOR database_name IN (' + @ColumnName + ')) AS PVTTable

UNION ALL

SELECT measurement = ''Rows max size (8KB pages)'', servername = REPLACE(@@SERVERNAME, ''\'', '':''), type = ''Database size''
, ' + @ColumnName + '  FROM
(
SELECT database_name, database_max_size_8k_pages
FROM #baseline
WHERE datafile_type = ''ROWS''
) as V
PIVOT(SUM(database_max_size_8k_pages) FOR database_name IN (' + @ColumnName + ')) AS PVTTable

UNION ALL

SELECT measurement = ''Logs max size (8KB pages)'', servername = REPLACE(@@SERVERNAME, ''\'', '':''), type = ''Database size''
, ' + @ColumnName + '  FROM
(
SELECT database_name, database_max_size_8k_pages
FROM #baseline
WHERE datafile_type = ''LOG''
) as V
PIVOT(SUM(database_max_size_8k_pages) FOR database_name IN (' + @ColumnName + ')) AS PVTTable
'

EXEC sp_executesql @DynamicPivotQuery;
`

const sqlDatabaseStats string = `SET DEADLOCK_PRIORITY -10;
SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

IF OBJECT_ID('tempdb..#baseline') IS NOT NULL
	DROP TABLE #baseline;

SELECT
[ReadLatency] =
    CASE WHEN [num_of_reads] = 0
        THEN 0 ELSE ([io_stall_read_ms] / [num_of_reads]) END,
[WriteLatency] =
    CASE WHEN [num_of_writes] = 0
        THEN 0 ELSE ([io_stall_write_ms] / [num_of_writes]) END,
[Latency] =
    CASE WHEN ([num_of_reads] = 0 AND [num_of_writes] = 0)
        THEN 0 ELSE ([io_stall] / ([num_of_reads] + [num_of_writes])) END,
[AvgBytesPerRead] =
    CASE WHEN [num_of_reads] = 0
        THEN 0 ELSE ([num_of_bytes_read] / [num_of_reads]) END,
[AvgBytesPerWrite] =
    CASE WHEN [num_of_writes] = 0
        THEN 0 ELSE ([num_of_bytes_written] / [num_of_writes]) END,
[AvgBytesPerTransfer] =
    CASE WHEN ([num_of_reads] = 0 AND [num_of_writes] = 0)
        THEN 0 ELSE
            (([num_of_bytes_read] + [num_of_bytes_written]) /
            ([num_of_reads] + [num_of_writes])) END,
DB_NAME ([vfs].[database_id]) AS DatabaseName,
[mf].type_desc  as datafile_type
INTO #baseline
FROM sys.dm_io_virtual_file_stats (NULL,NULL) AS [vfs]
JOIN sys.master_files AS [mf] ON [vfs].[database_id] = [mf].[database_id]
    AND [vfs].[file_id] = [mf].[file_id]



DECLARE @DynamicPivotQuery AS NVARCHAR(MAX)
DECLARE @ColumnName AS NVARCHAR(MAX), @ColumnName2 AS NVARCHAR(MAX)

SELECT @ColumnName= ISNULL(@ColumnName + ',','') + QUOTENAME(DatabaseName)
FROM (SELECT DISTINCT DatabaseName FROM #baseline) AS bl

--Prepare the PIVOT query using the dynamic
SET @DynamicPivotQuery = N'
SELECT measurement = ''Log read latency (ms)'', servername = REPLACE(@@SERVERNAME, ''\'', '':''), type = ''Database stats''
, ' + @ColumnName + '  FROM
(
SELECT DatabaseName,  ReadLatency
FROM #baseline
WHERE datafile_type = ''LOG''
) as V
PIVOT(MAX(ReadLatency) FOR DatabaseName IN (' + @ColumnName + ')) AS PVTTable

UNION ALL

SELECT measurement = ''Log write latency (ms)'', servername = REPLACE(@@SERVERNAME, ''\'', '':''), type = ''Database stats''
, ' + @ColumnName + '  FROM
(
SELECT DatabaseName,  WriteLatency
FROM #baseline
WHERE datafile_type = ''LOG''
) as V
PIVOT(MAX(WriteLatency) FOR DatabaseName IN (' + @ColumnName + ')) AS PVTTable

UNION ALL

SELECT measurement = ''Rows read latency (ms)'', servername = REPLACE(@@SERVERNAME, ''\'', '':''), type = ''Database stats''
, ' + @ColumnName + '  FROM
(
SELECT DatabaseName,  ReadLatency
FROM #baseline
WHERE datafile_type = ''ROWS''
) as V
PIVOT(MAX(ReadLatency) FOR DatabaseName IN (' + @ColumnName + ')) AS PVTTable

UNION ALL

SELECT measurement = ''Rows write latency (ms)'', servername = REPLACE(@@SERVERNAME, ''\'', '':''), type = ''Database stats''
, ' + @ColumnName + '  FROM
(
SELECT DatabaseName,  WriteLatency
FROM #baseline
WHERE datafile_type = ''ROWS''
) as V
PIVOT(MAX(WriteLatency) FOR DatabaseName IN (' + @ColumnName + ')) AS PVTTable

UNION ALL

SELECT measurement = ''Rows (average bytes/read)'', servername = REPLACE(@@SERVERNAME, ''\'', '':''), type = ''Database stats''
, ' + @ColumnName + '  FROM
(
SELECT DatabaseName,  AvgBytesPerRead
FROM #baseline
WHERE datafile_type = ''ROWS''
) as V
PIVOT(SUM(AvgBytesPerRead) FOR DatabaseName IN (' + @ColumnName + ')) AS PVTTable

UNION ALL

SELECT measurement = ''Rows (average bytes/write)'', servername = REPLACE(@@SERVERNAME, ''\'', '':''), type = ''Database stats''
, ' + @ColumnName + '  FROM
(
SELECT DatabaseName,  AvgBytesPerWrite
FROM #baseline
WHERE datafile_type = ''ROWS''
) as V
PIVOT(SUM(AvgBytesPerWrite) FOR DatabaseName IN (' + @ColumnName + ')) AS PVTTable

UNION ALL

SELECT measurement = ''Log (average bytes/read)'', servername = REPLACE(@@SERVERNAME, ''\'', '':''), type = ''Database stats''
, ' + @ColumnName + '  FROM
(
SELECT DatabaseName,  AvgBytesPerRead
FROM #baseline
WHERE datafile_type = ''LOG''
) as V
PIVOT(SUM(AvgBytesPerRead) FOR DatabaseName IN (' + @ColumnName + ')) AS PVTTable

UNION ALL

SELECT measurement = ''Log (average bytes/write)'', servername = REPLACE(@@SERVERNAME, ''\'', '':''), type = ''Database stats''
, ' + @ColumnName + '  FROM
(
SELECT DatabaseName,  AvgBytesPerWrite
FROM #baseline
WHERE datafile_type = ''LOG''
) as V
PIVOT(SUM(AvgBytesPerWrite) FOR DatabaseName IN (' + @ColumnName + ')) AS PVTTable
'

EXEC sp_executesql @DynamicPivotQuery;
`

const sqlDatabaseIO string = `SET DEADLOCK_PRIORITY -10;
SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
DECLARE @secondsBetween tinyint = 5;
DECLARE @delayInterval char(8) = CONVERT(Char(8), DATEADD(SECOND, @secondsBetween, '00:00:00'), 108);
IF OBJECT_ID('tempdb..#baseline') IS NOT NULL
	DROP TABLE #baseline;
IF OBJECT_ID('tempdb..#baselinewritten') IS NOT NULL
	DROP TABLE #baselinewritten;
SELECT DB_NAME(mf.database_id) AS databaseName ,
    mf.physical_name,
    divfs.num_of_bytes_read,
    divfs.num_of_bytes_written,
	divfs.num_of_reads,
	divfs.num_of_writes,
    GETDATE() AS baselinedate
INTO #baseline
FROM sys.dm_io_virtual_file_stats(NULL, NULL) AS divfs
INNER JOIN sys.master_files AS mf ON mf.database_id = divfs.database_id
	AND mf.file_id = divfs.file_id
WAITFOR DELAY @delayInterval;
;WITH currentLine AS
(
	SELECT DB_NAME(mf.database_id) AS databaseName ,
		type_desc,
		mf.physical_name,
		divfs.num_of_bytes_read,
		divfs.num_of_bytes_written,
		divfs.num_of_reads,
	    divfs.num_of_writes,
		GETDATE() AS currentlinedate
	FROM sys.dm_io_virtual_file_stats(NULL, NULL) AS divfs
	INNER JOIN sys.master_files AS mf ON mf.database_id = divfs.database_id
			AND mf.file_id = divfs.file_id
)
SELECT database_name
, datafile_type
, num_of_bytes_read_persec = SUM(num_of_bytes_read_persec)
, num_of_bytes_written_persec = SUM(num_of_bytes_written_persec)
, num_of_reads_persec = SUM(num_of_reads_persec)
, num_of_writes_persec = SUM(num_of_writes_persec)
INTO #baselinewritten
FROM
(
SELECT
  database_name = currentLine.databaseName
, datafile_type = type_desc
, num_of_bytes_read_persec = (currentLine.num_of_bytes_read - T1.num_of_bytes_read) / (DATEDIFF(SECOND,baselinedate,currentlinedate))
, num_of_bytes_written_persec = (currentLine.num_of_bytes_written - T1.num_of_bytes_written) / (DATEDIFF(SECOND,baselinedate,currentlinedate))
, num_of_reads_persec =  (currentLine.num_of_reads - T1.num_of_reads) / (DATEDIFF(SECOND,baselinedate,currentlinedate))
, num_of_writes_persec =  (currentLine.num_of_writes - T1.num_of_writes) / (DATEDIFF(SECOND,baselinedate,currentlinedate))
FROM currentLine
INNER JOIN #baseline T1 ON T1.databaseName = currentLine.databaseName
	AND T1.physical_name = currentLine.physical_name
) as T
GROUP BY database_name, datafile_type
DECLARE @DynamicPivotQuery AS NVARCHAR(MAX)
DECLARE @ColumnName AS NVARCHAR(MAX), @ColumnName2 AS NVARCHAR(MAX)
SELECT @ColumnName = ISNULL(@ColumnName + ',','') + QUOTENAME(database_name)
	FROM (SELECT DISTINCT database_name FROM #baselinewritten) AS bl
SELECT @ColumnName2 = ISNULL(@ColumnName2 + '+','') + QUOTENAME(database_name)
	FROM (SELECT DISTINCT database_name FROM #baselinewritten) AS bl
SET @DynamicPivotQuery = N'
SELECT measurement = ''Log writes (bytes/sec)'', servername = REPLACE(@@SERVERNAME, ''\'', '':''), type = ''Database IO''
, ' + @ColumnName + ', Total = ' + @ColumnName2 + ' FROM
(
SELECT database_name, num_of_bytes_written_persec
FROM #baselinewritten
WHERE datafile_type = ''LOG''
) as V
PIVOT(SUM(num_of_bytes_written_persec) FOR database_name IN (' + @ColumnName + ')) AS PVTTable
UNION ALL
SELECT measurement = ''Rows writes (bytes/sec)'', servername = REPLACE(@@SERVERNAME, ''\'', '':''), type = ''Database IO''
, ' + @ColumnName + ', Total = ' + @ColumnName2 + ' FROM
(
SELECT database_name, num_of_bytes_written_persec
FROM #baselinewritten
WHERE datafile_type = ''ROWS''
) as V
PIVOT(SUM(num_of_bytes_written_persec) FOR database_name IN (' + @ColumnName + ')) AS PVTTable
UNION ALL
SELECT measurement = ''Log reads (bytes/sec)'', servername = REPLACE(@@SERVERNAME, ''\'', '':''), type = ''Database IO''
, ' + @ColumnName + ', Total = ' + @ColumnName2 + ' FROM
(
SELECT database_name, num_of_bytes_read_persec
FROM #baselinewritten
WHERE datafile_type = ''LOG''
) as V
PIVOT(SUM(num_of_bytes_read_persec) FOR database_name IN (' + @ColumnName + ')) AS PVTTable
UNION ALL
SELECT measurement = ''Rows reads (bytes/sec)'', servername = REPLACE(@@SERVERNAME, ''\'', '':''), type = ''Database IO''
, ' + @ColumnName + ', Total = ' + @ColumnName2 + ' FROM
(
SELECT database_name, num_of_bytes_read_persec
FROM #baselinewritten
WHERE datafile_type = ''ROWS''
) as V
PIVOT(SUM(num_of_bytes_read_persec) FOR database_name IN (' + @ColumnName + ')) AS PVTTable
UNION ALL
SELECT measurement = ''Log (writes/sec)'', servername = REPLACE(@@SERVERNAME, ''\'', '':''), type = ''Database IO''
, ' + @ColumnName + ', Total = ' + @ColumnName2 + ' FROM
(
SELECT database_name, num_of_writes_persec
FROM #baselinewritten
WHERE datafile_type = ''LOG''
) as V
PIVOT(SUM(num_of_writes_persec) FOR database_name IN (' + @ColumnName + ')) AS PVTTable
UNION ALL
SELECT measurement = ''Rows (writes/sec)'', servername = REPLACE(@@SERVERNAME, ''\'', '':''), type = ''Database IO''
, ' + @ColumnName + ', Total = ' + @ColumnName2 + ' FROM
(
SELECT database_name, num_of_writes_persec
FROM #baselinewritten
WHERE datafile_type = ''ROWS''
) as V
PIVOT(SUM(num_of_writes_persec) FOR database_name IN (' + @ColumnName + ')) AS PVTTable
UNION ALL
SELECT measurement = ''Log (reads/sec)'', servername = REPLACE(@@SERVERNAME, ''\'', '':''), type = ''Database IO''
, ' + @ColumnName + ', Total = ' + @ColumnName2 + ' FROM
(
SELECT database_name, num_of_reads_persec
FROM #baselinewritten
WHERE datafile_type = ''LOG''
) as V
PIVOT(SUM(num_of_reads_persec) FOR database_name IN (' + @ColumnName + ')) AS PVTTable
UNION ALL
SELECT measurement = ''Rows (reads/sec)'', servername = REPLACE(@@SERVERNAME, ''\'', '':''), type = ''Database IO''
, ' + @ColumnName + ', Total = ' + @ColumnName2 + ' FROM
(
SELECT database_name, num_of_reads_persec
FROM #baselinewritten
WHERE datafile_type = ''ROWS''
) as V
PIVOT(SUM(num_of_reads_persec) FOR database_name IN (' + @ColumnName + ')) AS PVTTable
'
EXEC sp_executesql @DynamicPivotQuery;
`

const sqlDatabaseProperties string = `SET DEADLOCK_PRIORITY -10;
SET NOCOUNT ON;
SET ARITHABORT ON;
SET QUOTED_IDENTIFIER ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED

IF OBJECT_ID('tempdb..#Databases') IS NOT NULL
	DROP TABLE #Databases;
CREATE  TABLE #Databases
(
	Measurement nvarchar(64) NOT NULL,
	DatabaseName nvarchar(128) NOT NULL,
	Value tinyint NOT NULL
	Primary Key(DatabaseName, Measurement)
);

INSERT #Databases (	Measurement, DatabaseName, Value)
SELECT
  Measurement = 'Recovery Model FULL'
, DatabaseName = d.Name
, Value = CASE WHEN d.recovery_model = 1 THEN 1 ELSE 0 END
FROM sys.databases d
UNION ALL
SELECT
  Measurement = 'Recovery Model BULK_LOGGED'
, DatabaseName = d.Name
, Value = CASE WHEN d.recovery_model = 2 THEN 1 ELSE 0 END
FROM sys.databases d
UNION ALL
SELECT
  Measurement = 'Recovery Model SIMPLE'
, DatabaseName = d.Name
, Value = CASE WHEN d.recovery_model = 3 THEN 1 ELSE 0 END
FROM sys.databases d

UNION ALL
SELECT
  Measurement = 'State ONLINE'
, DatabaseName = d.Name
, Value = CASE WHEN d.state = 0 THEN 1 ELSE 0 END
FROM sys.databases d
UNION ALL
SELECT
  Measurement = 'State RESTORING'
, DatabaseName = d.Name
, Value = CASE WHEN d.state = 1 THEN 1 ELSE 0 END
FROM sys.databases d
UNION ALL
SELECT
  Measurement = 'State RECOVERING'
, DatabaseName = d.Name
, Value = CASE WHEN d.state = 2 THEN 1 ELSE 0 END
FROM sys.databases d
UNION ALL
SELECT
  Measurement = 'State RECOVERY_PENDING'
, DatabaseName = d.Name
, Value = CASE WHEN d.state = 3 THEN 1 ELSE 0 END
FROM sys.databases d
UNION ALL
SELECT
  Measurement = 'State SUSPECT'
, DatabaseName = d.Name
, Value = CASE WHEN d.state = 4 THEN 1 ELSE 0 END
FROM sys.databases d
UNION ALL
SELECT
  Measurement = 'State EMERGENCY'
, DatabaseName = d.Name
, Value = CASE WHEN d.state = 5 THEN 1 ELSE 0 END
FROM sys.databases d
UNION ALL
SELECT
  Measurement = 'State OFFLINE'
, DatabaseName = d.Name
, Value = CASE WHEN d.state = 6 THEN 1 ELSE 0 END
FROM sys.databases d

DECLARE @DynamicPivotQuery AS NVARCHAR(MAX)
DECLARE @ColumnName AS NVARCHAR(MAX)
SELECT @ColumnName= ISNULL(@ColumnName + ',','') + QUOTENAME(DatabaseName)
FROM (SELECT DISTINCT DatabaseName FROM #Databases) AS bl

SET @DynamicPivotQuery = N'
SELECT measurement = Measurement, servername = REPLACE(@@SERVERNAME, ''\'', '':'')
, type = ''Database properties''
, ' + @ColumnName + ', Total FROM
(
SELECT Measurement, DatabaseName, Value
, Total = (SELECT SUM(Value) FROM #Databases WHERE Measurement = d.Measurement)
FROM #Databases d
WHERE d.Measurement = ''Recovery Model FULL''
) as V
PIVOT(SUM(Value) FOR DatabaseName IN (' + @ColumnName + ')) AS PVTTable

UNION ALL

SELECT measurement = Measurement, servername = REPLACE(@@SERVERNAME, ''\'', '':'')
, type = ''Database properties''
, ' + @ColumnName + ', Total FROM
(
SELECT Measurement, DatabaseName, Value
, Total = (SELECT SUM(Value) FROM #Databases WHERE Measurement = d.Measurement)
FROM #Databases d
WHERE d.Measurement = ''Recovery Model BULK_LOGGED''
) as V
PIVOT(SUM(Value) FOR DatabaseName IN (' + @ColumnName + ')) AS PVTTable

UNION ALL

SELECT measurement = Measurement, servername = REPLACE(@@SERVERNAME, ''\'', '':'')
, type = ''Database properties''
, ' + @ColumnName + ', Total FROM
(
SELECT Measurement, DatabaseName, Value
, Total = (SELECT SUM(Value) FROM #Databases WHERE Measurement = d.Measurement)
FROM #Databases d
WHERE d.Measurement = ''Recovery Model SIMPLE''
) as V
PIVOT(SUM(Value) FOR DatabaseName IN (' + @ColumnName + ')) AS PVTTable


UNION ALL

SELECT measurement = Measurement, servername = REPLACE(@@SERVERNAME, ''\'', '':'')
, type = ''Database properties''
, ' + @ColumnName + ', Total FROM
(
SELECT Measurement, DatabaseName, Value
, Total = (SELECT SUM(Value) FROM #Databases WHERE Measurement = d.Measurement)
FROM #Databases d
WHERE d.Measurement = ''State ONLINE''
) as V
PIVOT(SUM(Value) FOR DatabaseName IN (' + @ColumnName + ')) AS PVTTable

UNION ALL

SELECT measurement = Measurement, servername = REPLACE(@@SERVERNAME, ''\'', '':'')
, type = ''Database properties''
, ' + @ColumnName + ', Total FROM
(
SELECT Measurement, DatabaseName, Value
, Total = (SELECT SUM(Value) FROM #Databases WHERE Measurement = d.Measurement)
FROM #Databases d
WHERE d.Measurement = ''State RESTORING''
) as V
PIVOT(SUM(Value) FOR DatabaseName IN (' + @ColumnName + ')) AS PVTTable

UNION ALL

SELECT measurement = Measurement, servername = REPLACE(@@SERVERNAME, ''\'', '':'')
, type = ''Database properties''
, ' + @ColumnName + ', Total FROM
(
SELECT Measurement, DatabaseName, Value
, Total = (SELECT SUM(Value) FROM #Databases WHERE Measurement = d.Measurement)
FROM #Databases d
WHERE d.Measurement = ''State RECOVERING''
) as V
PIVOT(SUM(Value) FOR DatabaseName IN (' + @ColumnName + ')) AS PVTTable

UNION ALL

SELECT measurement = Measurement, servername = REPLACE(@@SERVERNAME, ''\'', '':'')
, type = ''Database properties''
, ' + @ColumnName + ', Total FROM
(
SELECT Measurement, DatabaseName, Value
, Total = (SELECT SUM(Value) FROM #Databases WHERE Measurement = d.Measurement)
FROM #Databases d
WHERE d.Measurement = ''State RECOVERY_PENDING''
) as V
PIVOT(SUM(Value) FOR DatabaseName IN (' + @ColumnName + ')) AS PVTTable

UNION ALL

SELECT measurement = Measurement, servername = REPLACE(@@SERVERNAME, ''\'', '':'')
, type = ''Database properties''
, ' + @ColumnName + ', Total FROM
(
SELECT Measurement, DatabaseName, Value
, Total = (SELECT SUM(Value) FROM #Databases WHERE Measurement = d.Measurement)
FROM #Databases d
WHERE d.Measurement = ''State SUSPECT''
) as V
PIVOT(SUM(Value) FOR DatabaseName IN (' + @ColumnName + ')) AS PVTTable

UNION ALL

SELECT measurement = Measurement, servername = REPLACE(@@SERVERNAME, ''\'', '':'')
, type = ''Database properties''
, ' + @ColumnName + ', Total FROM
(
SELECT Measurement, DatabaseName, Value
, Total = (SELECT SUM(Value) FROM #Databases WHERE Measurement = d.Measurement)
FROM #Databases d
WHERE d.Measurement = ''State EMERGENCY''
) as V
PIVOT(SUM(Value) FOR DatabaseName IN (' + @ColumnName + ')) AS PVTTable

UNION ALL

SELECT measurement = Measurement, servername = REPLACE(@@SERVERNAME, ''\'', '':'')
, type = ''Database properties''
, ' + @ColumnName + ', Total FROM
(
SELECT Measurement, DatabaseName, Value
, Total = (SELECT SUM(Value) FROM #Databases WHERE Measurement = d.Measurement)
FROM #Databases d
WHERE d.Measurement = ''State OFFLINE''
) as V
PIVOT(SUM(Value) FOR DatabaseName IN (' + @ColumnName + ')) AS PVTTable
'
EXEC sp_executesql @DynamicPivotQuery;
`

const sqlCPUHistory string = `SET DEADLOCK_PRIORITY -10;
SET NOCOUNT ON;
SET ARITHABORT ON;
SET QUOTED_IDENTIFIER ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

DECLARE @ms_ticks bigint;
SET @ms_ticks = (Select ms_ticks From sys.dm_os_sys_info);
DECLARE @maxEvents int = 1

SELECT
---- measurement
  measurement = 'CPU (%)'
---- tags
, servername= REPLACE(@@SERVERNAME, '\', ':')
, type = 'CPU usage'
-- value
, [SQL process] = ProcessUtilization
, [External process]= 100 - SystemIdle - ProcessUtilization
, [SystemIdle]
FROM
(
SELECT TOP (@maxEvents)
  EventTime = CAST(DateAdd(ms, -1 * (@ms_ticks - timestamp_ms), GetUTCDate()) as datetime)
, ProcessUtilization = CAST(ProcessUtilization as int)
, SystemIdle = CAST(SystemIdle as int)
FROM (SELECT Record.value('(./Record/SchedulerMonitorEvent/SystemHealth/SystemIdle)[1]', 'int') as SystemIdle,
		     Record.value('(./Record/SchedulerMonitorEvent/SystemHealth/ProcessUtilization)[1]', 'int') as ProcessUtilization,
		     timestamp as timestamp_ms
FROM (SELECT timestamp, convert(xml, record) As Record
		FROM sys.dm_os_ring_buffers
		WHERE ring_buffer_type = N'RING_BUFFER_SCHEDULER_MONITOR'
		    And record Like '%<SystemHealth>%') x) y
ORDER BY timestamp_ms Desc
) as T;
`

const sqlPerformanceCounters string = `SET DEADLOCK_PRIORITY -10;
SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
IF OBJECT_ID('tempdb..#PCounters') IS NOT NULL DROP TABLE #PCounters
CREATE TABLE #PCounters
(
	object_name nvarchar(128),
	counter_name nvarchar(128),
	instance_name nvarchar(128),
	cntr_value bigint,
	cntr_type INT,
	Primary Key(object_name, counter_name, instance_name)
);
INSERT #PCounters
SELECT DISTINCT RTrim(spi.object_name) object_name
, RTrim(spi.counter_name) counter_name
, RTrim(spi.instance_name) instance_name
, spi.cntr_value
, spi.cntr_type
FROM sys.dm_os_performance_counters spi
WHERE spi.object_name NOT LIKE '%Backup Device%'
	AND NOT EXISTS (SELECT 1 FROM sys.databases WHERE Name = spi.instance_name);

WAITFOR DELAY '00:00:01';

IF OBJECT_ID('tempdb..#CCounters') IS NOT NULL DROP TABLE #CCounters
CREATE TABLE #CCounters
(
	object_name nvarchar(128),
	counter_name nvarchar(128),
	instance_name nvarchar(128),
	cntr_value bigint,
	cntr_type INT,
	Primary Key(object_name, counter_name, instance_name)
);
INSERT #CCounters
SELECT DISTINCT RTrim(spi.object_name) object_name
, RTrim(spi.counter_name) counter_name
, RTrim(spi.instance_name) instance_name
, spi.cntr_value
, spi.cntr_type
FROM sys.dm_os_performance_counters spi
WHERE spi.object_name NOT LIKE '%Backup Device%'
	AND NOT EXISTS (SELECT 1 FROM sys.databases WHERE Name = spi.instance_name);

SELECT
 measurement = cc.counter_name
	+ CASE WHEN LEN(cc.instance_name) > 0 THEN ' | ' + cc.instance_name ELSE '' END
	+ ' | '
	+ SUBSTRING( cc.object_name, CHARINDEX(':',  cc.object_name) + 1, LEN( cc.object_name) - CHARINDEX(':',  cc.object_name))
-- tags
, servername = REPLACE(@@SERVERNAME, '\', ':')
, type = 'Performance counters'
--, countertype = CASE cc.cntr_type
--    When 65792 Then 'Count'
--    When 537003264 Then 'Ratio'
--    When 272696576 Then 'Per second'
--    When 1073874176 Then 'Average'
--    When 272696320 Then 'Average Per Second'
--    When 1073939712 Then 'Base'
--    END
-- value
, value = CAST(CASE cc.cntr_type
    When 65792 Then cc.cntr_value -- Count
    When 537003264 Then IsNull(Cast(cc.cntr_value as decimal(19,4)) / NullIf(cbc.cntr_value, 0), 0) -- Ratio
    When 272696576 Then cc.cntr_value - pc.cntr_value -- Per Second
    When 1073874176 Then IsNull(Cast(cc.cntr_value - pc.cntr_value as decimal(19,4)) / NullIf(cbc.cntr_value - pbc.cntr_value, 0), 0) -- Avg
    When 272696320 Then IsNull(Cast(cc.cntr_value - pc.cntr_value as decimal(19,4)) / NullIf(cbc.cntr_value - pbc.cntr_value, 0), 0) -- Avg/sec
    When 1073939712 Then cc.cntr_value - pc.cntr_value -- Base
    Else cc.cntr_value End as bigint)
--, currentvalue= CAST(cc.cntr_value as bigint)
FROM #CCounters cc
INNER JOIN #PCounters pc On cc.object_name = pc.object_name
        And cc.counter_name = pc.counter_name
        And cc.instance_name = pc.instance_name
        And cc.cntr_type = pc.cntr_type
LEFT JOIN #CCounters cbc On cc.object_name = cbc.object_name
        And (Case When cc.counter_name Like '%(ms)' Then Replace(cc.counter_name, ' (ms)',' Base')
                  When cc.object_name like '%FileTable' Then Replace(cc.counter_name, 'Avg ','') + ' base'
                  When cc.counter_name = 'Worktables From Cache Ratio' Then 'Worktables From Cache Base'
                  When cc.counter_name = 'Avg. Length of Batched Writes' Then 'Avg. Length of Batched Writes BS'
                  Else cc.counter_name + ' base'
             End) = cbc.counter_name
        And cc.instance_name = cbc.instance_name
        And cc.cntr_type In (537003264, 1073874176)
        And cbc.cntr_type = 1073939712
LEFT JOIN #PCounters pbc On pc.object_name = pbc.object_name
        And pc.instance_name = pbc.instance_name
        And (Case When pc.counter_name Like '%(ms)' Then Replace(pc.counter_name, ' (ms)',' Base')
                  When pc.object_name like '%FileTable' Then Replace(pc.counter_name, 'Avg ','') + ' base'
                  When pc.counter_name = 'Worktables From Cache Ratio' Then 'Worktables From Cache Base'
                  When pc.counter_name = 'Avg. Length of Batched Writes' Then 'Avg. Length of Batched Writes BS'
                  Else pc.counter_name + ' base'
             End) = pbc.counter_name
        And pc.cntr_type In (537003264, 1073874176)

IF OBJECT_ID('tempdb..#CCounters') IS NOT NULL DROP TABLE #CCounters;
IF OBJECT_ID('tempdb..#PCounters') IS NOT NULL DROP TABLE #PCounters;
`

const sqlWaitStatsCategorized string = `SET DEADLOCK_PRIORITY -10;
SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED
DECLARE @secondsBetween tinyint = 5
DECLARE @delayInterval char(8) = CONVERT(Char(8), DATEADD(SECOND, @secondsBetween, '00:00:00'), 108);

DECLARE @w1 TABLE
(
	WaitType nvarchar(64) collate SQL_Latin1_General_CP1_CI_AS NOT NULL,
	WaitTimeInMs bigint NOT NULL,
	WaitTaskCount bigint NOT NULL,
	CollectionDate datetime NOT NULL
)
DECLARE @w2 TABLE
(
	WaitType nvarchar(64) collate SQL_Latin1_General_CP1_CI_AS NOT NULL,
	WaitTimeInMs bigint NOT NULL,
	WaitTaskCount bigint NOT NULL,
	CollectionDate datetime NOT NULL
)
DECLARE @w3 TABLE
(
	WaitType nvarchar(64) collate SQL_Latin1_General_CP1_CI_AS NOT NULL
)
DECLARE @w4 TABLE
(
	WaitType nvarchar(64) collate SQL_Latin1_General_CP1_CI_AS NOT NULL,
	WaitCategory nvarchar(64) collate SQL_Latin1_General_CP1_CI_AS NOT NULL
)
DECLARE @w5 TABLE
(
	WaitCategory nvarchar(64) collate SQL_Latin1_General_CP1_CI_AS NOT NULL,
	WaitTimeInMs bigint NOT NULL,
	WaitTaskCount bigint NOT NULL
)

INSERT @w3 (WaitType)
VALUES (N'QDS_SHUTDOWN_QUEUE'), (N'HADR_FILESTREAM_IOMGR_IOCOMPLETION'),
	(N'BROKER_EVENTHANDLER'),            (N'BROKER_RECEIVE_WAITFOR'),
	(N'BROKER_TASK_STOP'),               (N'BROKER_TO_FLUSH'),
	(N'BROKER_TRANSMITTER'),             (N'CHECKPOINT_QUEUE'),
	(N'CHKPT'),                          (N'CLR_AUTO_EVENT'),
	(N'CLR_MANUAL_EVENT'),               (N'CLR_SEMAPHORE'),
	(N'DBMIRROR_DBM_EVENT'),             (N'DBMIRROR_EVENTS_QUEUE'),
	(N'DBMIRROR_WORKER_QUEUE'),          (N'DBMIRRORING_CMD'),
	(N'DIRTY_PAGE_POLL'),                (N'DISPATCHER_QUEUE_SEMAPHORE'),
	(N'EXECSYNC'),                       (N'FSAGENT'),
	(N'FT_IFTS_SCHEDULER_IDLE_WAIT'),    (N'FT_IFTSHC_MUTEX'),
	(N'HADR_CLUSAPI_CALL'),              (N'HADR_FILESTREAM_IOMGR_IOCOMPLETION'),
	(N'HADR_LOGCAPTURE_WAIT'),           (N'HADR_NOTIFICATION_DEQUEUE'),
	(N'HADR_TIMER_TASK'),                (N'HADR_WORK_QUEUE'),
	(N'KSOURCE_WAKEUP'),                 (N'LAZYWRITER_SLEEP'),
	(N'LOGMGR_QUEUE'),                   (N'ONDEMAND_TASK_QUEUE'),
	(N'PWAIT_ALL_COMPONENTS_INITIALIZED'),
	(N'QDS_PERSIST_TASK_MAIN_LOOP_SLEEP'),
	(N'QDS_CLEANUP_STALE_QUERIES_TASK_MAIN_LOOP_SLEEP'),
	(N'REQUEST_FOR_DEADLOCK_SEARCH'),    (N'RESOURCE_QUEUE'),
	(N'SERVER_IDLE_CHECK'),              (N'SLEEP_BPOOL_FLUSH'),
	(N'SLEEP_DBSTARTUP'),                (N'SLEEP_DCOMSTARTUP'),
	(N'SLEEP_MASTERDBREADY'),            (N'SLEEP_MASTERMDREADY'),
	(N'SLEEP_MASTERUPGRADED'),           (N'SLEEP_MSDBSTARTUP'),
	(N'SLEEP_SYSTEMTASK'),               (N'SLEEP_TASK'),
	(N'SLEEP_TEMPDBSTARTUP'),            (N'SNI_HTTP_ACCEPT'),
	(N'SP_SERVER_DIAGNOSTICS_SLEEP'),    (N'SQLTRACE_BUFFER_FLUSH'),
	(N'SQLTRACE_INCREMENTAL_FLUSH_SLEEP'),
	(N'SQLTRACE_WAIT_ENTRIES'),          (N'WAIT_FOR_RESULTS'),
	(N'WAITFOR'),                        (N'WAITFOR_TASKSHUTDOW(N'),
	(N'WAIT_XTP_HOST_WAIT'),             (N'WAIT_XTP_OFFLINE_CKPT_NEW_LOG'),
	(N'WAIT_XTP_CKPT_CLOSE'),            (N'XE_DISPATCHER_JOI(N'),
	(N'XE_DISPATCHER_WAIT'),             (N'XE_TIMER_EVENT');

INSERT @w4 (WaitType, WaitCategory) VALUES ('ABR', 'OTHER') ,
('ASSEMBLY_LOAD' , 'OTHER') , ('ASYNC_DISKPOOL_LOCK' , 'I/O') , ('ASYNC_IO_COMPLETION' , 'I/O') ,
('ASYNC_NETWORK_IO' , 'NETWORK') , ('AUDIT_GROUPCACHE_LOCK' , 'OTHER') , ('AUDIT_LOGINCACHE_LOCK' ,
'OTHER') , ('AUDIT_ON_DEMAND_TARGET_LOCK' , 'OTHER') , ('AUDIT_XE_SESSION_MGR' , 'OTHER') , ('BACKUP' ,
'BACKUP') , ('BACKUP_CLIENTLOCK ' , 'BACKUP') , ('BACKUP_OPERATOR' , 'BACKUP') , ('BACKUPBUFFER' ,
'BACKUP') , ('BACKUPIO' , 'BACKUP') , ('BACKUPTHREAD' , 'BACKUP') , ('BAD_PAGE_PROCESS' , 'MEMORY') ,
('BROKER_CONNECTION_RECEIVE_TASK' , 'SERVICE BROKER') , ('BROKER_ENDPOINT_STATE_MUTEX' , 'SERVICE BROKER')
, ('BROKER_EVENTHANDLER' , 'SERVICE BROKER') , ('BROKER_INIT' , 'SERVICE BROKER') , ('BROKER_MASTERSTART'
, 'SERVICE BROKER') , ('BROKER_RECEIVE_WAITFOR' , 'SERVICE BROKER') , ('BROKER_REGISTERALLENDPOINTS' ,
'SERVICE BROKER') , ('BROKER_SERVICE' , 'SERVICE BROKER') , ('BROKER_SHUTDOWN' , 'SERVICE BROKER') ,
('BROKER_TASK_STOP' , 'SERVICE BROKER') , ('BROKER_TO_FLUSH' , 'SERVICE BROKER') , ('BROKER_TRANSMITTER' ,
'SERVICE BROKER') , ('BUILTIN_HASHKEY_MUTEX' , 'OTHER') , ('CHECK_PRINT_RECORD' , 'OTHER') ,
('CHECKPOINT_QUEUE' , 'BUFFER') , ('CHKPT' , 'BUFFER') , ('CLEAR_DB' , 'OTHER') , ('CLR_AUTO_EVENT' ,
'CLR') , ('CLR_CRST' , 'CLR') , ('CLR_JOIN' , 'CLR') , ('CLR_MANUAL_EVENT' , 'CLR') , ('CLR_MEMORY_SPY' ,
'CLR') , ('CLR_MONITOR' , 'CLR') , ('CLR_RWLOCK_READER' , 'CLR') , ('CLR_RWLOCK_WRITER' , 'CLR') ,
('CLR_SEMAPHORE' , 'CLR') , ('CLR_TASK_START' , 'CLR') , ('CLRHOST_STATE_ACCESS' , 'CLR') , ('CMEMTHREAD'
, 'MEMORY') , ('COMMIT_TABLE' , 'OTHER') , ('CURSOR' , 'OTHER') , ('CURSOR_ASYNC' , 'OTHER') , ('CXPACKET'
, 'OTHER') , ('CXROWSET_SYNC' , 'OTHER') , ('DAC_INIT' , 'OTHER') , ('DBMIRROR_DBM_EVENT ' , 'OTHER') ,
('DBMIRROR_DBM_MUTEX ' , 'OTHER') , ('DBMIRROR_EVENTS_QUEUE' , 'OTHER') , ('DBMIRROR_SEND' , 'OTHER') ,
('DBMIRROR_WORKER_QUEUE' , 'OTHER') , ('DBMIRRORING_CMD' , 'OTHER') , ('DBTABLE' , 'OTHER') ,
('DEADLOCK_ENUM_MUTEX' , 'LOCK') , ('DEADLOCK_TASK_SEARCH' , 'LOCK') , ('DEBUG' , 'OTHER') ,
('DISABLE_VERSIONING' , 'OTHER') , ('DISKIO_SUSPEND' , 'BACKUP') , ('DISPATCHER_QUEUE_SEMAPHORE' ,
'OTHER') , ('DLL_LOADING_MUTEX' , 'XML') , ('DROPTEMP' , 'TEMPORARY OBJECTS') , ('DTC' , 'OTHER') ,
('DTC_ABORT_REQUEST' , 'OTHER') , ('DTC_RESOLVE' , 'OTHER') , ('DTC_STATE' , 'DOTHERTC') ,
('DTC_TMDOWN_REQUEST' , 'OTHER') , ('DTC_WAITFOR_OUTCOME' , 'OTHER') , ('DUMP_LOG_COORDINATOR' , 'OTHER')
, ('DUMP_LOG_COORDINATOR_QUEUE' , 'OTHER') , ('DUMPTRIGGER' , 'OTHER') , ('EC' , 'OTHER') , ('EE_PMOLOCK'
, 'MEMORY') , ('EE_SPECPROC_MAP_INIT' , 'OTHER') , ('ENABLE_VERSIONING' , 'OTHER') ,
('ERROR_REPORTING_MANAGER' , 'OTHER') , ('EXCHANGE' , 'OTHER') , ('EXECSYNC' , 'OTHER') ,
('EXECUTION_PIPE_EVENT_OTHER' , 'OTHER') , ('Failpoint' , 'OTHER') , ('FCB_REPLICA_READ' , 'OTHER') ,
('FCB_REPLICA_WRITE' , 'OTHER') , ('FS_FC_RWLOCK' , 'OTHER') , ('FS_GARBAGE_COLLECTOR_SHUTDOWN' , 'OTHER')
, ('FS_HEADER_RWLOCK' , 'OTHER') , ('FS_LOGTRUNC_RWLOCK' , 'OTHER') , ('FSA_FORCE_OWN_XACT' , 'OTHER') ,
('FSAGENT' , 'OTHER') , ('FSTR_CONFIG_MUTEX' , 'OTHER') , ('FSTR_CONFIG_RWLOCK' , 'OTHER') ,
('FT_COMPROWSET_RWLOCK' , 'OTHER') , ('FT_IFTS_RWLOCK' , 'OTHER') , ('FT_IFTS_SCHEDULER_IDLE_WAIT' ,
'OTHER') , ('FT_IFTSHC_MUTEX' , 'OTHER') , ('FT_IFTSISM_MUTEX' , 'OTHER') , ('FT_MASTER_MERGE' , 'OTHER')
, ('FT_METADATA_MUTEX' , 'OTHER') , ('FT_RESTART_CRAWL' , 'OTHER') , ('FT_RESUME_CRAWL' , 'OTHER') ,
('FULLTEXT GATHERER' , 'OTHER') , ('GUARDIAN' , 'OTHER') , ('HTTP_ENDPOINT_COLLCREATE' , 'SERVICE BROKER')
, ('HTTP_ENUMERATION' , 'SERVICE BROKER') , ('HTTP_START' , 'SERVICE BROKER') , ('IMP_IMPORT_MUTEX' ,
'OTHER') , ('IMPPROV_IOWAIT' , 'I/O') , ('INDEX_USAGE_STATS_MUTEX' , 'OTHER') , ('OTHER_TESTING' ,
'OTHER') , ('IO_AUDIT_MUTEX' , 'OTHER') , ('IO_COMPLETION' , 'I/O') , ('IO_RETRY' , 'I/O') ,
('IOAFF_RANGE_QUEUE' , 'OTHER') , ('KSOURCE_WAKEUP' , 'SHUTDOWN') , ('KTM_ENLISTMENT' , 'OTHER') ,
('KTM_RECOVERY_MANAGER' , 'OTHER') , ('KTM_RECOVERY_RESOLUTION' , 'OTHER') , ('LATCH_DT' , 'LATCH') ,
('LATCH_EX' , 'LATCH') , ('LATCH_KP' , 'LATCH') , ('LATCH_NL' , 'LATCH') , ('LATCH_SH' , 'LATCH') ,
('LATCH_UP' , 'LATCH') , ('LAZYWRITER_SLEEP' , 'BUFFER') , ('LCK_M_BU' , 'LOCK') , ('LCK_M_IS' , 'LOCK') ,
('LCK_M_IU' , 'LOCK') , ('LCK_M_IX' , 'LOCK') , ('LCK_M_RIn_NL' , 'LOCK') , ('LCK_M_RIn_S' , 'LOCK') ,
('LCK_M_RIn_U' , 'LOCK') , ('LCK_M_RIn_X' , 'LOCK') , ('LCK_M_RS_S' , 'LOCK') , ('LCK_M_RS_U' , 'LOCK') ,
('LCK_M_RX_S' , 'LOCK') , ('LCK_M_RX_U' , 'LOCK') , ('LCK_M_RX_X' , 'LOCK') , ('LCK_M_S' , 'LOCK') ,
('LCK_M_SCH_M' , 'LOCK') , ('LCK_M_SCH_S' , 'LOCK') , ('LCK_M_SIU' , 'LOCK') , ('LCK_M_SIX' , 'LOCK') ,
('LCK_M_U' , 'LOCK') , ('LCK_M_UIX' , 'LOCK') , ('LCK_M_X' , 'LOCK') , ('LOGBUFFER' , 'OTHER') ,
('LOGGENERATION' , 'OTHER') , ('LOGMGR' , 'OTHER') , ('LOGMGR_FLUSH' , 'OTHER') , ('LOGMGR_QUEUE' ,
'OTHER') , ('LOGMGR_RESERVE_APPEND' , 'OTHER') , ('LOWFAIL_MEMMGR_QUEUE' , 'MEMORY') ,
('METADATA_LAZYCACHE_RWLOCK' , 'OTHER') , ('MIRROR_SEND_MESSAGE' , 'OTHER') , ('MISCELLANEOUS' , 'IGNORE')
, ('MSQL_DQ' , 'DISTRIBUTED QUERY') , ('MSQL_SYNC_PIPE' , 'OTHER') , ('MSQL_XACT_MGR_MUTEX' , 'OTHER') ,
('MSQL_XACT_MUTEX' , 'OTHER') , ('MSQL_XP' , 'OTHER') , ('MSSEARCH' , 'OTHER') , ('NET_WAITFOR_PACKET' ,
'NETWORK') , ('NODE_CACHE_MUTEX' , 'OTHER') , ('OTHER' , 'OTHER') , ('ONDEMAND_TASK_QUEUE' , 'OTHER') ,
('PAGEIOLATCH_DT' , 'LATCH') , ('PAGEIOLATCH_EX' , 'LATCH') , ('PAGEIOLATCH_KP' , 'LATCH') ,
('PAGEIOLATCH_NL' , 'LATCH') , ('PAGEIOLATCH_SH' , 'LATCH') , ('PAGEIOLATCH_UP' , 'LATCH') ,
('PAGELATCH_DT' , 'LATCH') , ('PAGELATCH_EX' , 'LATCH') , ('PAGELATCH_KP' , 'LATCH') , ('PAGELATCH_NL' ,
'LATCH') , ('PAGELATCH_SH' , 'LATCH') , ('PAGELATCH_UP' , 'LATCH') , ('PARALLEL_BACKUP_QUEUE' , 'BACKUP')
, ('PERFORMANCE_COUNTERS_RWLOCK' , 'OTHER') , ('PREEMPTIVE_ABR' , 'OTHER') ,
('PREEMPTIVE_AUDIT_ACCESS_EVENTLOG' , 'OTHER') , ('PREEMPTIVE_AUDIT_ACCESS_SECLOG' , 'OTHER') ,
('PREEMPTIVE_CLOSEBACKUPMEDIA' , 'OTHER') , ('PREEMPTIVE_CLOSEBACKUPTAPE' , 'OTHER') ,
('PREEMPTIVE_CLOSEBACKUPVDIDEVICE' , 'OTHER') , ('PREEMPTIVE_CLUSAPI_CLUSTERRESOURCECONTROL' , 'OTHER') ,
('PREEMPTIVE_COM_COCREATEINSTANCE' , 'OTHER') , ('PREEMPTIVE_COM_COGETCLASSOBJECT' , 'OTHER') ,
('PREEMPTIVE_COM_CREATEACCESSOR' , 'OTHER') , ('PREEMPTIVE_COM_DELETEROWS' , 'OTHER') ,
('PREEMPTIVE_COM_GETCOMMANDTEXT' , 'OTHER') , ('PREEMPTIVE_COM_GETDATA' , 'OTHER') ,
('PREEMPTIVE_COM_GETNEXTROWS' , 'OTHER') , ('PREEMPTIVE_COM_GETRESULT' , 'OTHER') ,
('PREEMPTIVE_COM_GETROWSBYBOOKMARK' , 'OTHER') , ('PREEMPTIVE_COM_LBFLUSH' , 'OTHER') ,
('PREEMPTIVE_COM_LBLOCKREGION' , 'OTHER') , ('PREEMPTIVE_COM_LBREADAT' , 'OTHER') ,
('PREEMPTIVE_COM_LBSETSIZE' , 'OTHER') , ('PREEMPTIVE_COM_LBSTAT' , 'OTHER') ,
('PREEMPTIVE_COM_LBUNLOCKREGION' , 'OTHER') , ('PREEMPTIVE_COM_LBWRITEAT' , 'OTHER') ,
('PREEMPTIVE_COM_QUERYINTERFACE' , 'OTHER') , ('PREEMPTIVE_COM_RELEASE' , 'OTHER') ,
('PREEMPTIVE_COM_RELEASEACCESSOR' , 'OTHER') , ('PREEMPTIVE_COM_RELEASEROWS' , 'OTHER') ,
('PREEMPTIVE_COM_RELEASESESSION' , 'OTHER') , ('PREEMPTIVE_COM_RESTARTPOSITION' , 'OTHER') ,
('PREEMPTIVE_COM_SEQSTRMREAD' , 'OTHER') , ('PREEMPTIVE_COM_SEQSTRMREADANDWRITE' , 'OTHER') ,
('PREEMPTIVE_COM_SETDATAFAILURE' , 'OTHER') , ('PREEMPTIVE_COM_SETPARAMETERINFO' , 'OTHER') ,
('PREEMPTIVE_COM_SETPARAMETERPROPERTIES' , 'OTHER') , ('PREEMPTIVE_COM_STRMLOCKREGION' , 'OTHER') ,
('PREEMPTIVE_COM_STRMSEEKANDREAD' , 'OTHER') , ('PREEMPTIVE_COM_STRMSEEKANDWRITE' , 'OTHER') ,
('PREEMPTIVE_COM_STRMSETSIZE' , 'OTHER') , ('PREEMPTIVE_COM_STRMSTAT' , 'OTHER') ,
('PREEMPTIVE_COM_STRMUNLOCKREGION' , 'OTHER') , ('PREEMPTIVE_CONSOLEWRITE' , 'OTHER') ,
('PREEMPTIVE_CREATEPARAM' , 'OTHER') , ('PREEMPTIVE_DEBUG' , 'OTHER') , ('PREEMPTIVE_DFSADDLINK' ,
'OTHER') , ('PREEMPTIVE_DFSLINKEXISTCHECK' , 'OTHER') , ('PREEMPTIVE_DFSLINKHEALTHCHECK' , 'OTHER') ,
('PREEMPTIVE_DFSREMOVELINK' , 'OTHER') , ('PREEMPTIVE_DFSREMOVEROOT' , 'OTHER') ,
('PREEMPTIVE_DFSROOTFOLDERCHECK' , 'OTHER') , ('PREEMPTIVE_DFSROOTINIT' , 'OTHER') ,
('PREEMPTIVE_DFSROOTSHARECHECK' , 'OTHER') , ('PREEMPTIVE_DTC_ABORT' , 'OTHER') ,
('PREEMPTIVE_DTC_ABORTREQUESTDONE' , 'OTHER') , ('PREEMPTIVE_DTC_BEGINOTHER' , 'OTHER') ,
('PREEMPTIVE_DTC_COMMITREQUESTDONE' , 'OTHER') , ('PREEMPTIVE_DTC_ENLIST' , 'OTHER') ,
('PREEMPTIVE_DTC_PREPAREREQUESTDONE' , 'OTHER') , ('PREEMPTIVE_FILESIZEGET' , 'OTHER') ,
('PREEMPTIVE_FSAOTHER_ABORTOTHER' , 'OTHER') , ('PREEMPTIVE_FSAOTHER_COMMITOTHER' , 'OTHER') ,
('PREEMPTIVE_FSAOTHER_STARTOTHER' , 'OTHER') , ('PREEMPTIVE_FSRECOVER_UNCONDITIONALUNDO' , 'OTHER') ,
('PREEMPTIVE_GETRMINFO' , 'OTHER') , ('PREEMPTIVE_LOCKMONITOR' , 'OTHER') , ('PREEMPTIVE_MSS_RELEASE' ,
'OTHER') , ('PREEMPTIVE_ODBCOPS' , 'OTHER') , ('PREEMPTIVE_OLE_UNINIT' , 'OTHER') ,
('PREEMPTIVE_OTHER_ABORTORCOMMITTRAN' , 'OTHER') , ('PREEMPTIVE_OTHER_ABORTTRAN' , 'OTHER') ,
('PREEMPTIVE_OTHER_GETDATASOURCE' , 'OTHER') , ('PREEMPTIVE_OTHER_GETLITERALINFO' , 'OTHER') ,
('PREEMPTIVE_OTHER_GETPROPERTIES' , 'OTHER') , ('PREEMPTIVE_OTHER_GETPROPERTYINFO' , 'OTHER') ,
('PREEMPTIVE_OTHER_GETSCHEMALOCK' , 'OTHER') , ('PREEMPTIVE_OTHER_JOINOTHER' , 'OTHER') ,
('PREEMPTIVE_OTHER_RELEASE' , 'OTHER') , ('PREEMPTIVE_OTHER_SETPROPERTIES' , 'OTHER') ,
('PREEMPTIVE_OTHEROPS' , 'OTHER') , ('PREEMPTIVE_OS_ACCEPTSECURITYCONTEXT' , 'OTHER') ,
('PREEMPTIVE_OS_ACQUIRECREDENTIALSHANDLE' , 'OTHER') , ('PREEMPTIVE_OS_AU,TICATIONOPS' , 'OTHER') ,
('PREEMPTIVE_OS_AUTHORIZATIONOPS' , 'OTHER') , ('PREEMPTIVE_OS_AUTHZGETINFORMATIONFROMCONTEXT' , 'OTHER')
, ('PREEMPTIVE_OS_AUTHZINITIALIZECONTEXTFROMSID' , 'OTHER') ,
('PREEMPTIVE_OS_AUTHZINITIALIZERESOURCEMANAGER' , 'OTHER') , ('PREEMPTIVE_OS_BACKUPREAD' , 'OTHER') ,
('PREEMPTIVE_OS_CLOSEHANDLE' , 'OTHER') , ('PREEMPTIVE_OS_CLUSTEROPS' , 'OTHER') , ('PREEMPTIVE_OS_COMOPS'
, 'OTHER') , ('PREEMPTIVE_OS_COMPLETEAUTHTOKEN' , 'OTHER') , ('PREEMPTIVE_OS_COPYFILE' , 'OTHER') ,
('PREEMPTIVE_OS_CREATEDIRECTORY' , 'OTHER') , ('PREEMPTIVE_OS_CREATEFILE' , 'OTHER') ,
('PREEMPTIVE_OS_CRYPTACQUIRECONTEXT' , 'OTHER') , ('PREEMPTIVE_OS_CRYPTIMPORTKEY' , 'OTHER') ,
('PREEMPTIVE_OS_CRYPTOPS' , 'OTHER') , ('PREEMPTIVE_OS_DECRYPTMESSAGE' , 'OTHER') ,
('PREEMPTIVE_OS_DELETEFILE' , 'OTHER') , ('PREEMPTIVE_OS_DELETESECURITYCONTEXT' , 'OTHER') ,
('PREEMPTIVE_OS_DEVICEIOCONTROL' , 'OTHER') , ('PREEMPTIVE_OS_DEVICEOPS' , 'OTHER') ,
('PREEMPTIVE_OS_DIRSVC_NETWORKOPS' , 'OTHER') , ('PREEMPTIVE_OS_DISCONNECTNAMEDPIPE' , 'OTHER') ,
('PREEMPTIVE_OS_DOMAINSERVICESOPS' , 'OTHER') , ('PREEMPTIVE_OS_DSGETDCNAME' , 'OTHER') ,
('PREEMPTIVE_OS_DTCOPS' , 'OTHER') , ('PREEMPTIVE_OS_ENCRYPTMESSAGE' , 'OTHER') , ('PREEMPTIVE_OS_FILEOPS'
, 'OTHER') , ('PREEMPTIVE_OS_FINDFILE' , 'OTHER') , ('PREEMPTIVE_OS_FLUSHFILEBUFFERS' , 'OTHER') ,
('PREEMPTIVE_OS_FORMATMESSAGE' , 'OTHER') , ('PREEMPTIVE_OS_FREECREDENTIALSHANDLE' , 'OTHER') ,
('PREEMPTIVE_OS_FREELIBRARY' , 'OTHER') , ('PREEMPTIVE_OS_GENERICOPS' , 'OTHER') ,
('PREEMPTIVE_OS_GETADDRINFO' , 'OTHER') , ('PREEMPTIVE_OS_GETCOMPRESSEDFILESIZE' , 'OTHER') ,
('PREEMPTIVE_OS_GETDISKFREESPACE' , 'OTHER') , ('PREEMPTIVE_OS_GETFILEATTRIBUTES' , 'OTHER') ,
('PREEMPTIVE_OS_GETFILESIZE' , 'OTHER') , ('PREEMPTIVE_OS_GETLONGPATHNAME' , 'OTHER') ,
('PREEMPTIVE_OS_GETPROCADDRESS' , 'OTHER') , ('PREEMPTIVE_OS_GETVOLUMENAMEFORVOLUMEMOUNTPOINT' , 'OTHER')
, ('PREEMPTIVE_OS_GETVOLUMEPATHNAME' , 'OTHER') , ('PREEMPTIVE_OS_INITIALIZESECURITYCONTEXT' , 'OTHER') ,
('PREEMPTIVE_OS_LIBRARYOPS' , 'OTHER') , ('PREEMPTIVE_OS_LOADLIBRARY' , 'OTHER') ,
('PREEMPTIVE_OS_LOGONUSER' , 'OTHER') , ('PREEMPTIVE_OS_LOOKUPACCOUNTSID' , 'OTHER') ,
('PREEMPTIVE_OS_MESSAGEQUEUEOPS' , 'OTHER') , ('PREEMPTIVE_OS_MOVEFILE' , 'OTHER') ,
('PREEMPTIVE_OS_NETGROUPGETUSERS' , 'OTHER') , ('PREEMPTIVE_OS_NETLOCALGROUPGETMEMBERS' , 'OTHER') ,
('PREEMPTIVE_OS_NETUSERGETGROUPS' , 'OTHER') , ('PREEMPTIVE_OS_NETUSERGETLOCALGROUPS' , 'OTHER') ,
('PREEMPTIVE_OS_NETUSERMODALSGET' , 'OTHER') , ('PREEMPTIVE_OS_NETVALIDATEPASSWORDPOLICY' , 'OTHER') ,
('PREEMPTIVE_OS_NETVALIDATEPASSWORDPOLICYFREE' , 'OTHER') , ('PREEMPTIVE_OS_OPENDIRECTORY' , 'OTHER') ,
('PREEMPTIVE_OS_PIPEOPS' , 'OTHER') , ('PREEMPTIVE_OS_PROCESSOPS' , 'OTHER') ,
('PREEMPTIVE_OS_QUERYREGISTRY' , 'OTHER') , ('PREEMPTIVE_OS_QUERYSECURITYCONTEXTTOKEN' , 'OTHER') ,
('PREEMPTIVE_OS_REMOVEDIRECTORY' , 'OTHER') , ('PREEMPTIVE_OS_REPORTEVENT' , 'OTHER') ,
('PREEMPTIVE_OS_REVERTTOSELF' , 'OTHER') , ('PREEMPTIVE_OS_RSFXDEVICEOPS' , 'OTHER') ,
('PREEMPTIVE_OS_SECURITYOPS' , 'OTHER') , ('PREEMPTIVE_OS_SERVICEOPS' , 'OTHER') ,
('PREEMPTIVE_OS_SETENDOFFILE' , 'OTHER') , ('PREEMPTIVE_OS_SETFILEPOINTER' , 'OTHER') ,
('PREEMPTIVE_OS_SETFILEVALIDDATA' , 'OTHER') , ('PREEMPTIVE_OS_SETNAMEDSECURITYINFO' , 'OTHER') ,
('PREEMPTIVE_OS_SQLCLROPS' , 'OTHER') , ('PREEMPTIVE_OS_SQMLAUNCH' , 'OTHER') ,
('PREEMPTIVE_OS_VERIFYSIGNATURE' , 'OTHER') , ('PREEMPTIVE_OS_VSSOPS' , 'OTHER') ,
('PREEMPTIVE_OS_WAITFORSINGLEOBJECT' , 'OTHER') , ('PREEMPTIVE_OS_WINSOCKOPS' , 'OTHER') ,
('PREEMPTIVE_OS_WRITEFILE' , 'OTHER') , ('PREEMPTIVE_OS_WRITEFILEGATHER' , 'OTHER') ,
('PREEMPTIVE_OS_WSASETLASTERROR' , 'OTHER') , ('PREEMPTIVE_REENLIST' , 'OTHER') , ('PREEMPTIVE_RESIZELOG'
, 'OTHER') , ('PREEMPTIVE_ROLLFORWARDREDO' , 'OTHER') , ('PREEMPTIVE_ROLLFORWARDUNDO' , 'OTHER') ,
('PREEMPTIVE_SB_STOPENDPOINT' , 'OTHER') , ('PREEMPTIVE_SERVER_STARTUP' , 'OTHER') ,
('PREEMPTIVE_SETRMINFO' , 'OTHER') , ('PREEMPTIVE_SHAREDMEM_GETDATA' , 'OTHER') , ('PREEMPTIVE_SNIOPEN' ,
'OTHER') , ('PREEMPTIVE_SOSHOST' , 'OTHER') , ('PREEMPTIVE_SOSTESTING' , 'OTHER') , ('PREEMPTIVE_STARTRM'
, 'OTHER') , ('PREEMPTIVE_STREAMFCB_CHECKPOINT' , 'OTHER') , ('PREEMPTIVE_STREAMFCB_RECOVER' , 'OTHER') ,
('PREEMPTIVE_STRESSDRIVER' , 'OTHER') , ('PREEMPTIVE_TESTING' , 'OTHER') , ('PREEMPTIVE_TRANSIMPORT' ,
'OTHER') , ('PREEMPTIVE_UNMARSHALPROPAGATIONTOKEN' , 'OTHER') , ('PREEMPTIVE_VSS_CREATESNAPSHOT' ,
'OTHER') , ('PREEMPTIVE_VSS_CREATEVOLUMESNAPSHOT' , 'OTHER') , ('PREEMPTIVE_XE_CALLBACKEXECUTE' , 'OTHER')
, ('PREEMPTIVE_XE_DISPATCHER' , 'OTHER') , ('PREEMPTIVE_XE_ENGINEINIT' , 'OTHER') ,
('PREEMPTIVE_XE_GETTARGETSTATE' , 'OTHER') , ('PREEMPTIVE_XE_SESSIONCOMMIT' , 'OTHER') ,
('PREEMPTIVE_XE_TARGETFINALIZE' , 'OTHER') , ('PREEMPTIVE_XE_TARGETINIT' , 'OTHER') ,
('PREEMPTIVE_XE_TIMERRUN' , 'OTHER') , ('PREEMPTIVE_XETESTING' , 'OTHER') , ('PREEMPTIVE_XXX' , 'OTHER') ,
('PRINT_ROLLBACK_PROGRESS' , 'OTHER') , ('QNMANAGER_ACQUIRE' , 'OTHER') , ('QPJOB_KILL' , 'OTHER') ,
('QPJOB_WAITFOR_ABORT' , 'OTHER') , ('QRY_MEM_GRANT_INFO_MUTEX' , 'OTHER') , ('QUERY_ERRHDL_SERVICE_DONE'
, 'OTHER') , ('QUERY_EXECUTION_INDEX_SORT_EVENT_OPEN' , 'OTHER') , ('QUERY_NOTIFICATION_MGR_MUTEX' ,
'OTHER') , ('QUERY_NOTIFICATION_SUBSCRIPTION_MUTEX' , 'OTHER') , ('QUERY_NOTIFICATION_TABLE_MGR_MUTEX' ,
'OTHER') , ('QUERY_NOTIFICATION_UNITTEST_MUTEX' , 'OTHER') , ('QUERY_OPTIMIZER_PRINT_MUTEX' , 'OTHER') ,
('QUERY_TRACEOUT' , 'OTHER') , ('QUERY_WAIT_ERRHDL_SERVICE' , 'OTHER') , ('RECOVER_CHANGEDB' , 'OTHER') ,
('REPL_CACHE_ACCESS' , 'REPLICATION') , ('REPL_HISTORYCACHE_ACCESS' , 'OTHER') , ('REPL_SCHEMA_ACCESS' ,
'OTHER') , ('REPL_TRANHASHTABLE_ACCESS' , 'OTHER') , ('REPLICA_WRITES' , 'OTHER') ,
('REQUEST_DISPENSER_PAUSE' , 'BACKUP') , ('REQUEST_FOR_DEADLOCK_SEARCH' , 'LOCK') , ('RESMGR_THROTTLED' ,
'OTHER') , ('RESOURCE_QUERY_SEMAPHORE_COMPILE' , 'QUERY') , ('RESOURCE_QUEUE' , 'OTHER') ,
('RESOURCE_SEMAPHORE' , 'OTHER') , ('RESOURCE_SEMAPHORE_MUTEX' , 'MEMORY') ,
('RESOURCE_SEMAPHORE_QUERY_COMPILE' , 'MEMORY') , ('RESOURCE_SEMAPHORE_SMALL_QUERY' , 'MEMORY') ,
('RG_RECONFIG' , 'OTHER') , ('SEC_DROP_TEMP_KEY' , 'SECURITY') , ('SECURITY_MUTEX' , 'OTHER') ,
('SEQUENTIAL_GUID' , 'OTHER') , ('SERVER_IDLE_CHECK' , 'OTHER') , ('SHUTDOWN' , 'OTHER') ,
('SLEEP_BPOOL_FLUSH' , 'OTHER') , ('SLEEP_DBSTARTUP' , 'OTHER') , ('SLEEP_DCOMSTARTUP' , 'OTHER') ,
('SLEEP_MSDBSTARTUP' , 'OTHER') , ('SLEEP_SYSTEMTASK' , 'OTHER') , ('SLEEP_TASK' , 'OTHER') ,
('SLEEP_TEMPDBSTARTUP' , 'OTHER') , ('SNI_CRITICAL_SECTION' , 'OTHER') , ('SNI_HTTP_ACCEPT' , 'OTHER') ,
('SNI_HTTP_WAITFOR_0_DISCON' , 'OTHER') , ('SNI_LISTENER_ACCESS' , 'OTHER') , ('SNI_TASK_COMPLETION' ,
'OTHER') , ('SOAP_READ' , 'OTHER') , ('SOAP_WRITE' , 'OTHER') , ('SOS_CALLBACK_REMOVAL' , 'OTHER') ,
('SOS_DISPATCHER_MUTEX' , 'OTHER') , ('SOS_LOCALALLOCATORLIST' , 'OTHER') , ('SOS_MEMORY_USAGE_ADJUSTMENT'
, 'OTHER') , ('SOS_OBJECT_STORE_DESTROY_MUTEX' , 'OTHER') , ('SOS_PROCESS_AFFINITY_MUTEX' , 'OTHER') ,
('SOS_RESERVEDMEMBLOCKLIST' , 'OTHER') , ('SOS_SCHEDULER_YIELD' , 'SQLOS') , ('SOS_SMALL_PAGE_ALLOC' ,
'OTHER') , ('SOS_STACKSTORE_INIT_MUTEX' , 'OTHER') , ('SOS_SYNC_TASK_ENQUEUE_EVENT' , 'OTHER') ,
('SOS_VIRTUALMEMORY_LOW' , 'OTHER') , ('SOSHOST_EVENT' , 'CLR') , ('SOSHOST_OTHER' , 'CLR') ,
('SOSHOST_MUTEX' , 'CLR') , ('SOSHOST_ROWLOCK' , 'CLR') , ('SOSHOST_RWLOCK' , 'CLR') ,
('SOSHOST_SEMAPHORE' , 'CLR') , ('SOSHOST_SLEEP' , 'CLR') , ('SOSHOST_TRACELOCK' , 'CLR') ,
('SOSHOST_WAITFORDONE' , 'CLR') , ('SQLCLR_APPDOMAIN' , 'CLR') , ('SQLCLR_ASSEMBLY' , 'CLR') ,
('SQLCLR_DEADLOCK_DETECTION' , 'CLR') , ('SQLCLR_QUANTUM_PUNISHMENT' , 'CLR') , ('SQLSORT_NORMMUTEX' ,
'OTHER') , ('SQLSORT_SORTMUTEX' , 'OTHER') , ('SQLTRACE_BUFFER_FLUSH ' , 'TRACE') , ('SQLTRACE_LOCK' ,
'OTHER') , ('SQLTRACE_SHUTDOWN' , 'OTHER') , ('SQLTRACE_WAIT_ENTRIES' , 'OTHER') , ('SRVPROC_SHUTDOWN' ,
'OTHER') , ('TEMPOBJ' , 'OTHER') , ('THREADPOOL' , 'SQLOS') , ('TIMEPRIV_TIMEPERIOD' , 'OTHER') ,
('TRACE_EVTNOTIF' , 'OTHER') , ('TRACEWRITE' , 'OTHER') , ('TRAN_MARKLATCH_DT' , 'TRAN_MARKLATCH') ,
('TRAN_MARKLATCH_EX' , 'TRAN_MARKLATCH') , ('TRAN_MARKLATCH_KP' , 'TRAN_MARKLATCH') , ('TRAN_MARKLATCH_NL'
, 'TRAN_MARKLATCH') , ('TRAN_MARKLATCH_SH' , 'TRAN_MARKLATCH') , ('TRAN_MARKLATCH_UP' , 'TRAN_MARKLATCH')
, ('OTHER_MUTEX' , 'OTHER') , ('UTIL_PAGE_ALLOC' , 'OTHER') , ('VIA_ACCEPT' , 'OTHER') ,
('VIEW_DEFINITION_MUTEX' , 'OTHER') , ('WAIT_FOR_RESULTS' , 'OTHER') , ('WAITFOR' , 'WAITFOR') ,
('WAITFOR_TASKSHUTDOWN' , 'OTHER') , ('WAITSTAT_MUTEX' , 'OTHER') , ('WCC' , 'OTHER') , ('WORKTBL_DROP' ,
'OTHER') , ('WRITE_COMPLETION' , 'OTHER') , ('WRITELOG' , 'I/O') , ('XACT_OWN_OTHER' , 'OTHER') ,
('XACT_RECLAIM_SESSION' , 'OTHER') , ('XACTLOCKINFO' , 'OTHER') , ('XACTWORKSPACE_MUTEX' , 'OTHER') ,
('XE_BUFFERMGR_ALLPROCESSED_EVENT' , 'XEVENT') , ('XE_BUFFERMGR_FREEBUF_EVENT' , 'XEVENT') ,
('XE_DISPATCHER_CONFIG_SESSION_LIST' , 'XEVENT') , ('XE_DISPATCHER_JOIN' , 'XEVENT') ,
('XE_DISPATCHER_WAIT' , 'XEVENT') , ('XE_MODULEMGR_SYNC' , 'XEVENT') , ('XE_OLS_LOCK' , 'XEVENT') ,
('XE_PACKAGE_LOCK_BACKOFF' , 'XEVENT') , ('XE_SERVICES_EVENTMANUAL' , 'XEVENT') , ('XE_SERVICES_MUTEX' ,
'XEVENT') , ('XE_SERVICES_RWLOCK' , 'XEVENT') , ('XE_SESSION_CREATE_SYNC' , 'XEVENT') ,
('XE_SESSION_FLUSH' , 'XEVENT') , ('XE_SESSION_SYNC' , 'XEVENT') , ('XE_STM_CREATE' , 'XEVENT') ,
('XE_TIMER_EVENT' , 'XEVENT') , ('XE_TIMER_MUTEX' , 'XEVENT')
, ('XE_TIMER_TASK_DONE' , 'XEVENT');


INSERT @w1 (WaitType, WaitTimeInMs, WaitTaskCount, CollectionDate)
SELECT
  WaitType = wait_type  collate SQL_Latin1_General_CP1_CI_AS
, WaitTimeInMs = SUM(wait_time_ms)
, WaitTaskCount = SUM(waiting_tasks_count)
, CollectionDate = GETDATE()
FROM sys.dm_os_wait_stats
WHERE [wait_type]  collate SQL_Latin1_General_CP1_CI_AS NOT IN
(
	SELECT WaitType FROM  @w3
)
AND [waiting_tasks_count] > 0
GROUP BY wait_type

WAITFOR DELAY @delayInterval;

INSERT @w2 (WaitType, WaitTimeInMs, WaitTaskCount, CollectionDate)
SELECT
  WaitType = wait_type  collate SQL_Latin1_General_CP1_CI_AS
, WaitTimeInMs = SUM(wait_time_ms)
, WaitTaskCount = SUM(waiting_tasks_count)
, CollectionDate = GETDATE()
FROM sys.dm_os_wait_stats
WHERE [wait_type]  collate SQL_Latin1_General_CP1_CI_AS NOT IN
(
	SELECT WaitType FROM  @w3
)
AND [waiting_tasks_count] > 0
GROUP BY wait_type;


INSERT @w5 (WaitCategory, WaitTimeInMs, WaitTaskCount)
SELECT WaitCategory
, WaitTimeInMs = SUM(WaitTimeInMs)
, WaitTaskCount = SUM(WaitTaskCount)
FROM
(
SELECT
  WaitCategory = ISNULL(T4.WaitCategory, 'OTHER')
, WaitTimeInMs = (T2.WaitTimeInMs - T1.WaitTimeInMs)
, WaitTaskCount = (T2.WaitTaskCount - T1.WaitTaskCount)
--, WaitTimeInMsPerSec = ((T2.WaitTimeInMs - T1.WaitTimeInMs) / CAST(DATEDIFF(SECOND, T1.CollectionDate, T2.CollectionDate) as float))
FROM @w1 T1
INNER JOIN @w2 T2 ON T2.WaitType = T1.WaitType
LEFT JOIN @w4 T4 ON T4.WaitType = T1.WaitType
WHERE T2.WaitTaskCount - T1.WaitTaskCount > 0
) as G
GROUP BY G.WaitCategory;



SELECT
---- measurement
  measurement = 'Wait time (ms)'
---- tags
, servername= REPLACE(@@SERVERNAME, '\', ':')
, type = 'Wait stats'
---- values
, [I/O] = SUM([I/O])
, [Latch] = SUM([LATCH])
, [Lock] = SUM([LOCK])
, [Network] = SUM([NETWORK])
, [Service broker] = SUM([SERVICE BROKER])
, [Memory] = SUM([MEMORY])
, [Buffer] = SUM([BUFFER])
, [CLR] = SUM([CLR])
, [SQLOS] = SUM([SQLOS])
, [XEvent] = SUM([XEVENT])
, [Other] = SUM([OTHER])
, [Total] = SUM([I/O]+[LATCH]+[LOCK]+[NETWORK]+[SERVICE BROKER]+[MEMORY]+[BUFFER]+[CLR]+[XEVENT]+[SQLOS]+[OTHER])
FROM
(
SELECT
  [I/O] = ISNULL([I/O] , 0)
, [MEMORY] = ISNULL([MEMORY] , 0)
, [BUFFER] = ISNULL([BUFFER] , 0)
, [LATCH] = ISNULL([LATCH] , 0)
, [LOCK] = ISNULL([LOCK] , 0)
, [NETWORK] = ISNULL([NETWORK] , 0)
, [SERVICE BROKER] = ISNULL([SERVICE BROKER] , 0)
, [CLR] = ISNULL([CLR] , 0)
, [XEVENT] = ISNULL([XEVENT] , 0)
, [SQLOS] = ISNULL([SQLOS] , 0)
, [OTHER] = ISNULL([OTHER] , 0)
FROM @w5 as P
PIVOT
(
	SUM(WaitTimeInMs)
	FOR WaitCategory IN ([I/O], [LATCH], [LOCK], [NETWORK], [SERVICE BROKER], [MEMORY], [BUFFER], [CLR], [XEVENT], [SQLOS], [OTHER])
) AS PivotTable
) as T

UNION ALL

SELECT
---- measurement
  measurement = 'Wait tasks'
---- tags
, server_name= REPLACE(@@SERVERNAME, '\', ':')
, type = 'Wait stats'
---- values
, [I/O] = SUM([I/O])
, [Latch] = SUM([LATCH])
, [Lock] = SUM([LOCK])
, [Network] = SUM([NETWORK])
, [Service broker] = SUM([SERVICE BROKER])
, [Memory] = SUM([MEMORY])
, [Buffer] = SUM([BUFFER])
, [CLR] = SUM([CLR])
, [SQLOS] = SUM([SQLOS])
, [XEvent] = SUM([XEVENT])
, [Other] = SUM([OTHER])
, [Total] = SUM([I/O]+[LATCH]+[LOCK]+[NETWORK]+[SERVICE BROKER]+[MEMORY]+[BUFFER]+[CLR]+[XEVENT]+[SQLOS]+[OTHER])
FROM
(
SELECT
  [I/O] = ISNULL([I/O] , 0)
, [MEMORY] = ISNULL([MEMORY] , 0)
, [BUFFER] = ISNULL([BUFFER] , 0)
, [LATCH] = ISNULL([LATCH] , 0)
, [LOCK] = ISNULL([LOCK] , 0)
, [NETWORK] = ISNULL([NETWORK] , 0)
, [SERVICE BROKER] = ISNULL([SERVICE BROKER] , 0)
, [CLR] = ISNULL([CLR] , 0)
, [XEVENT] = ISNULL([XEVENT] , 0)
, [SQLOS] = ISNULL([SQLOS] , 0)
, [OTHER] = ISNULL([OTHER] , 0)
FROM @w5 as P
PIVOT
(
	SUM(WaitTaskCount)
	FOR WaitCategory IN ([I/O], [LATCH], [LOCK], [NETWORK], [SERVICE BROKER], [MEMORY], [BUFFER], [CLR], [XEVENT], [SQLOS], [OTHER])
) AS PivotTable
) as T;
`

const sqlVolumeSpace string = `SET DEADLOCK_PRIORITY -10;
SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

IF OBJECT_ID('tempdb..#volumestats') IS NOT NULL
	DROP TABLE #volumestats;
SELECT DISTINCT
  volume =  REPLACE(vs.volume_mount_point, '\', '')
	+ CASE WHEN LEN(vs.logical_volume_name) > 0
		THEN ' (' + vs.logical_volume_name + ')'
		ELSE '' END
, total_bytes = vs.total_bytes
, available_bytes = vs.available_bytes
, used_bytes = vs.total_bytes - vs.available_bytes
, used_percent = 100 * CAST(ROUND((vs.total_bytes - vs.available_bytes) * 1. / vs.total_bytes, 2) as decimal(5,2))
INTO #volumestats
FROM sys.master_files AS f
CROSS APPLY sys.dm_os_volume_stats(f.database_id, f.file_id) vs

DECLARE @DynamicPivotQuery AS NVARCHAR(MAX)
DECLARE @ColumnName AS NVARCHAR(MAX), @ColumnName2 AS NVARCHAR(MAX)

SELECT @ColumnName= ISNULL(@ColumnName + ',','') + QUOTENAME(volume)
FROM (SELECT DISTINCT volume FROM #volumestats) AS bl

--Prepare the PIVOT query using the dynamic
SET @DynamicPivotQuery = N'
SELECT measurement = ''Volume total space (bytes)'', servername = REPLACE(@@SERVERNAME, ''\'', '':''), type = ''OS Volume space''
, ' + @ColumnName + '  FROM
(
SELECT volume,  total_bytes
FROM #volumestats
) as V
PIVOT(SUM(total_bytes) FOR volume IN (' + @ColumnName + ')) AS PVTTable

UNION ALL

SELECT measurement = ''Volume available space (bytes)'', servername = REPLACE(@@SERVERNAME, ''\'', '':''), type = ''OS Volume space''
, ' + @ColumnName + '  FROM
(
SELECT volume,  available_bytes
FROM #volumestats
) as V
PIVOT(SUM(available_bytes) FOR volume IN (' + @ColumnName + ')) AS PVTTable

UNION ALL

SELECT measurement = ''Volume used space (bytes)'', servername = REPLACE(@@SERVERNAME, ''\'', '':''), type = ''OS Volume space''
, ' + @ColumnName + '  FROM
(
SELECT volume,  used_bytes
FROM #volumestats
) as V
PIVOT(SUM(used_bytes) FOR volume IN (' + @ColumnName + ')) AS PVTTable

UNION ALL

SELECT measurement = ''Volume used space (%)'', servername = REPLACE(@@SERVERNAME, ''\'', '':''), type = ''OS Volume space''
, ' + @ColumnName + '  FROM
(
SELECT volume,  used_percent
FROM #volumestats
) as V
PIVOT(SUM(used_percent) FOR volume IN (' + @ColumnName + ')) AS PVTTable'

EXEC sp_executesql @DynamicPivotQuery;
`
