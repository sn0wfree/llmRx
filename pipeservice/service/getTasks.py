# coding=utf-8
from pipeservice.utils.MySQLConn_v004_node import MySQLNode


class GetTasks(object):
    def __init__(self, table: str, db: str, visits_mysql_node_conf: dict, maxlen=10, ext_process=None):
        self.table = table
        self.db = db
        self.conn = self._create_conn(table, visits_mysql_node_conf)
        self.max_len = maxlen - 1 if maxlen > 1 else maxlen
        self.ext_process = ext_process

    @staticmethod
    def _create_conn(name, visits_mysql_node_conf):
        if isinstance(visits_mysql_node_conf, MySQLNode):
            conn = visits_mysql_node_conf
        else:
            conn = MySQLNode(name, **visits_mysql_node_conf)
        return conn

    def update_task(self, self_id):
        db = self.db
        table = self.table
        sql = f"UPDATE {db}.{table} SET `status` = '2'  WHERE  `self_id` = '{self_id}' ;"
        print(sql)
        self.conn.Excutesql(sql)

    def reset_tasks(self):
        print('will clean all incomplete tasks!')
        db = self.db
        table = self.table

        sql = f"UPDATE {db}.{table} SET `status` = '0' WHERE `status` = '1' ;"
        self.conn.Excutesql(sql)

    def __call__(self):
        db = self.db
        table = self.table
        max_len = self.max_len
        sql = f"select * from {db}.{table} where status = 0 limit {max_len}"
        df = self.conn.sql2data(sql).drop_duplicates()

        if df.empty:
            return None
        else:
            a = map(str, df['self_id'].values.tolist())
            self_id_list_str = "', '".join(a)
            update_sql = f"UPDATE {db}.{table} SET `status` = '1' WHERE {table}.`self_id` in ( '{self_id_list_str}' );"
            self.conn.Excutesql(update_sql)
            if self.ext_process is None:
                return df
            else:
                return self.ext_process(df)


def get_tasks(table: str, db: str, visits_mysql_node_conf: dict, maxlen=10, ext_process=None):
    max_len = maxlen - 1 if maxlen > 1 else maxlen

    def simple():
        if isinstance(visits_mysql_node_conf, MySQLNode):
            conn = visits_mysql_node_conf
        else:
            conn = MySQLNode('news', **visits_mysql_node_conf)

        sql = f"select * from {db}.{table} where status = 0 limit {max_len}"
        df = conn.sql2data(sql).drop_duplicates()
        if df.empty:
            return None
        else:
            a = map(str, df['self_id'].values.tolist())
            # print(a)
            self_id_list_str = "', '".join(a)
            update_sql = f"UPDATE {db}.{table} SET `status` = '1' WHERE {table}.`self_id` in ( '{self_id_list_str}' );"
            conn.Excutesql(update_sql)
            if ext_process is None:
                return df
            else:
                return ext_process(df)

    return simple


def reset_tasks(table: str, db: str, visits_mysql_node_conf: dict):
    print('will clean all incomplete tasks!')

    def simple():
        if isinstance(visits_mysql_node_conf, MySQLNode):
            conn = visits_mysql_node_conf
        else:
            conn = MySQLNode('news', **visits_mysql_node_conf)
        sql = f"UPDATE {db}.{table} SET `status` = '0' WHERE `status` = '1' ;"
        conn.Excutesql(sql)

    return simple


if __name__ == '__main__':
    from pipeservice.conf.conf import visits_mysql_node_conf
    reset_tasks('diplomacy_activities', 'visits', visits_mysql_node_conf)()
    pass
