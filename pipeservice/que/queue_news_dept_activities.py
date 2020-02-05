# coding=utf-8

from pipeservice.que import create_queue_type1

visits_mysql_node_conf = dict(host='106.13.205.210', port=3306, user='linlu', passwd='Imsn0wfree', db='visits')
db, table = 'visits', 'dept_activities'
name = table

Queue = create_queue_type1(name, table, db, visits_mysql_node_conf)
