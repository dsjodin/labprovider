module(load="imudp")
module(load="imtcp")

input(type="imudp" port="{{.SYSLOG_PORT}}")
input(type="imtcp" port="{{.SYSLOG_PORT}}")

template(name="PerHostLogs" type="string"
         string="{{.SYSLOG_LOG_DIR}}/%HOSTNAME%/%PROGRAMNAME%.log")

*.* action(type="omfile" dynaFile="PerHostLogs" createDirs="on")
