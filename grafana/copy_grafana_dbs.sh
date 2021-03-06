#!/bin/bash
if [ -z "$ONLY" ]
then
  host=`hostname`
  if [ $host = "cncftest.io" ]
  then
    all=`cat ./devel/all_test_projects.txt`
  else
    all=`cat ./devel/all_prod_projects.txt`
  fi
  all=${all/kubernetes/k8s}
else
  all=$ONLY
fi
for proj in $all
do
    echo $proj
    cp "/var/lib/grafana.${proj}/grafana.db" "/var/www/html/grafana.${proj}.db" || exit 1
done
echo 'OK'
