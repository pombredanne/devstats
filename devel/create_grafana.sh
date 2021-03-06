#!/bin/bash
# GET=1 (Get grafana.db from the test server)
# STOP=1 (Stops running grafana-server instance)
set -o pipefail
host=`hostname`
ga="google_analytics_ua_id = $GA"
if ( [ -z "$PG_PASS" ] || [ -z "$PORT" ] || [ -z "$GA" ] || [ -z "$ICON" ] || [ -z "$ORGNAME" ] || [ -z "$PROJ" ] || [ -z "$PROJDB" ] || [ -z "$GRAFSUFF" ] )
then
  echo "$0: You need to set PG_PASS, PROJ, PROJDB, PORT, GA, ICON, ORGNAME, GRAFSUFF environment variable to run this script"
  exit 1
fi

pid=`ps -axu | grep grafana-server | grep $GRAFSUFF | awk '{print $2}'`
if [ ! -z "$STOP" ]
then
  echo "stopping $PROJ grafana server instance"
  if [ ! -z "$pid" ]
  then
    echo "stopping pid $pid"
    kill $pid
  else
    echo "grafana-server $PROJ not running"
  fi
fi

pid=`ps -axu | grep grafana-server | grep $GRAFSUFF | awk '{print $2}'`
if [ ! -z "$pid" ]
then
  echo "$PROJ grafana-server is running, exiting"
  exit 0
fi

wd=`pwd`
cd ~/dev/cncf/artwork || exit 2
git pull || exit 3
cd $wd || exit 4

if [ ! -d "/usr/share/grafana.$GRAFSUFF/" ]
then
  echo "copying /usr/share/grafana.$GRAFSUFF/"
  cp -R ~/grafana.v5/usr.share.grafana "/usr/share/grafana.$GRAFSUFF"/ || exit 5
  if [ ! "$ICON" = "-" ]
  then
    icontype=`./devel/get_icon_type.sh "$PROJ"` || exit 6
    cp "$HOME/dev/cncf/artwork/$ICON/icon/$icontype/$ICON-icon-$icontype.svg" "/usr/share/grafana.$GRAFSUFF/public/img/grafana_icon.svg" || exit 7
    cp "$HOME/dev/cncf/artwork/$ICON/icon/$icontype/$ICON-icon-$icontype.svg" "/usr/share/grafana.$GRAFSUFF/public/img/grafana_com_auth_icon.svg" || exit 8
    cp "$HOME/dev/cncf/artwork/$ICON/icon/$icontype/$ICON-icon-$icontype.svg" "/usr/share/grafana.$GRAFSUFF/public/img/grafana_net_logo.svg" || exit 9
    cp "$HOME/dev/cncf/artwork/$ICON/icon/$icontype/$ICON-icon-$icontype.svg" "/usr/share/grafana.$GRAFSUFF/public/img/grafana_mask_icon.svg" || exit 10
    convert "$HOME/dev/cncf/artwork/$ICON/icon/$icontype/$ICON-icon-$icontype.png" -resize 80x80 "/var/www/html/img/$PROJ-icon-color.png" || exit 11
    cp "$HOME/dev/cncf/artwork/$ICON/icon/$icontype/$ICON-icon-$icontype.svg" "/var/www/html/img/$PROJ-icon-color.svg" || exit 12
    if [ ! -f "grafana/img/$GRAFSUFF.svg" ]
    then
      cp "$HOME/dev/cncf/artwork/$ICON/icon/$icontype/$ICON-icon-$icontype.svg" "grafana/img/$GRAFSUFF.svg" || exit 13
    fi
    if [ ! -f "grafana/img/${GRAFSUFF}32.png" ]
    then
      convert "$HOME/dev/cncf/artwork/$ICON/icon/$icontype/$ICON-icon-$icontype.png" -resize 32x32 "grafana/img/${GRAFSUFF}32.png" || exit 14
    fi
  fi
  GRAFANA_DATA="/usr/share/grafana.$GRAFSUFF/" ./grafana/$PROJ/change_title_and_icons.sh || exit 15
fi

if [ ! -d "/var/lib/grafana.$GRAFSUFF/" ]
then
  echo "copying /var/lib/grafana.$GRAFSUFF/"
  cp -R ~/grafana.v5/var.lib.grafana "/var/lib/grafana.$GRAFSUFF"/ || exit 16
  rm -f "/var/lib/grafana.$GRAFSUFF/grafana.db" || exit 17
fi
  
if ( [ ! -f "/var/lib/grafana.$GRAFSUFF/grafana.db" ] && [ ! -z "$GET" ] )
then
  echo "attempt to fetch grafana database $GRAFSUFF from the test server"
  wget "https://cncftest.io/grafana.$GRAFSUFF.db" || exit 18
  mv "grafana.$GRAFSUFF.db" "/var/lib/grafana.$GRAFSUFF/grafana.db" || exit 19
fi

if [ ! -d "/etc/grafana.$GRAFSUFF/" ]
then
  echo "copying /etc/grafana.$GRAFSUFF/"
  cp -R ~/grafana.v5/etc.grafana "/etc/grafana.$GRAFSUFF"/ || exit 20
  cfile="/etc/grafana.$GRAFSUFF/grafana.ini"
  cp ./grafana/etc/grafana.ini.example "$cfile" || exit 21
  MODE=ss FROM='{{project}}' TO="$PROJ" replacer "$cfile" || exit 22
  MODE=ss FROM='{{url}}' TO="$host" replacer "$cfile" || exit 23
  MODE=ss FROM='{{port}}' TO="$PORT" replacer "$cfile" || exit 24
  MODE=ss FROM='{{pwd}}' TO="$PG_PASS" replacer "$cfile" || exit 25
  MODE=ss FROM=';google_analytics_ua_id =' TO="-" replacer "$cfile" || exit 26
  if [ $host = "devstats.cncf.io" ]
  then
    MODE=ss FROM='{{ga}}' TO="$ga" replacer "$cfile" || exit 27
    MODE=ss FROM='{{test}}' TO="-" replacer "$cfile" || exit 28
  else
    MODE=ss FROM='{{ga}}' TO=";$ga" replacer "$cfile" || exit 29
    MODE=ss FROM='{{test}}' TO="_test" replacer "$cfile" || exit 30
  fi
  MODE=ss FROM='{{org}}' TO="$ORGNAME" replacer "$cfile" || exit 31
fi

exists=`sudo -u postgres psql -tAc "select 1 from pg_database WHERE datname = '${GRAFSUFF}_grafana_sessions'"` || exit 32
if [ ! "$exists" = "1" ]
then
  echo "creating grafana sessions database ${GRAFSUFF}_grafana_sessions"
  sudo -u postgres psql -c "create database ${GRAFSUFF}_grafana_sessions" || exit 33
  sudo -u postgres psql -c "grant all privileges on database \"${GRAFSUFF}_grafana_sessions\" to gha_admin" || exit 34
  sudo -u postgres psql "${GRAFSUFF}_grafana_sessions" < util_sql/grafana_session_table.sql || exit 35
else
  echo "grafana sessions database ${GRAFSUFF}_grafana_sessions already exists"
fi

pid=`ps -axu | grep grafana-server | grep $GRAFSUFF | awk '{print $2}'`
if [ -z "$pid" ]
then
  echo "starting $PROJ grafana-server"
  ./grafana/$PROJ/grafana_start.sh &
  echo "started"
fi
echo "$0: $PROJ finished"
