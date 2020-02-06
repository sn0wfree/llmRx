# coding=utf-8
from pipeservice.conf.conf import visits_mysql_node_conf
from pipeservice.que import create_queue_type1

db, table = 'visits', 'diplomacy_activities'
name = table

Queue = create_queue_type1(name, table, db, visits_mysql_node_conf)
