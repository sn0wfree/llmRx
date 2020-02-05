# coding=utf-8
import requests

CODE = 'asduashduhaskhaskhakhdksa'

from pipeservice.utils.MySQLConn_v004_node import MySQLNode

visits_mysql_node_conf = dict(host='106.13.205.210', port=3306, user='linlu', passwd='Imsn0wfree', db='visits')


def get_task(CODE, base_url="http://0.0.0.0:5000/diplomacy_activities/get/"):
    url = base_url + f'{CODE}'
    resp = requests.get(url)
    if resp.status_code == 200:
        return resp.text
    elif resp.status_code == 414:
        return 'not tasks'
    else:
        raise ValueError('get status code: {}  and status {}'.format(resp.status_code, resp.text))


if __name__ == '__main__':
    conn = MySQLNode('test', **visits_mysql_node_conf)

    a = get_task(CODE, base_url="http://0.0.0.0:5000/diplomacy_activities/get/")
    print(conn.sql2data('select count(*) from visits.diplomacy_activities where status =1 '))
    print(a)

    pass
