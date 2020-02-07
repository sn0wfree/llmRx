# coding=utf-8
import json
import time

import requests

from pipeservice.utils.MySQLConn_v004_node import MySQLNode


def get_task(CODE, base_url="http://0.0.0.0:5000/diplomacy_activities/get/"):
    url = base_url + '{}/{}'.format(CODE, int(time.time()))
    resp = requests.get(url)
    if resp.status_code == 200:
        try:
            return json.loads(resp.text)
        except Exception as e:
            print(e)
            return resp.text
    elif resp.status_code == 414:
        return 'not tasks'
    else:
        raise ValueError('get status code: {}  and status {}'.format(resp.status_code, resp.text))


def completed_task(CODE, self_id, res_sql, base_url="http://0.0.0.0:5000/diplomacy_activities/done/"):
    url = base_url + f'{self_id}/{CODE}'

    data = json.dumps({'sql': res_sql})
    resp = requests.post(url, json=data)
    if resp.status_code == 200:
        return resp.text
    elif resp.status_code == 414:
        return 'not tasks'
    else:
        raise ValueError('get status code: {}  and status {}'.format(resp.status_code, resp.text))


class TaskGrabber(object):
    def __init__(self, code, base_url):
        self.code = code
        self.base_url = base_url.rstrip('/')

    @staticmethod
    def _status_code_parser(resp):
        if resp.status_code == 200:
            try:
                return json.loads(resp.text)
            except Exception as e:
                print(e)
                return resp.text
        elif resp.status_code == 414:
            return 'not tasks'
        else:
            raise ValueError('get status code: {}  and status {}'.format(resp.status_code, resp.text))

    def get(self):
        import time
        base_url = self.base_url + '/get/'
        CODE = self.code
        url = base_url + '{}/{}'.format(CODE, int(time.time()))
        resp = requests.get(url)
        return self._status_code_parser(resp)

    def update(self, self_id, res_sql):
        base_url = self.base_url + '/done/'
        CODE = self.code
        url = base_url + f'{self_id}/{CODE}'
        data = json.dumps({'sql': res_sql})
        resp = requests.post(url, json=data)
        # resp = requests.get(url)
        return self._status_code_parser(resp)


if __name__ == '__main__':
    from pipeservice.conf.conf import visits_mysql_node_conf, CODE

    conn = MySQLNode('test', **visits_mysql_node_conf)
    # a = get_task(CODE, base_url="http://0.0.0.0:5000/diplomacy_activities/get/")
    t = TaskGrabber(CODE, base_url="http://0.0.0.0:5000/dept_activities/")
    a = t.get()
    # print(conn.sql2data('select count(*) from visits.diplomacy_activities where status =1 '))
    print(a)

    pass
