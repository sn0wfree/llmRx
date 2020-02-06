# coding=utf-8
import requests


from pipeservice.utils.MySQLConn_v004_node import MySQLNode


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
    from pipeservice.conf.conf import visits_mysql_node_conf,CODE

    conn = MySQLNode('test', **visits_mysql_node_conf)

    a = get_task(CODE, base_url="http://0.0.0.0:5000/diplomacy_activities/get/")
    print(conn.sql2data('select count(*) from visits.diplomacy_activities where status =1 '))
    print(a)

    pass
