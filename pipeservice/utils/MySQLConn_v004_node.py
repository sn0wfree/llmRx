# -*- coding:utf-8 -*-
import os
import random
import subprocess
import time
from collections import namedtuple

import pandas as pd
import pymysql
import sqlalchemy

__version__ = '0.0.4'
__Author__ = 'sn0wfree and gg'

"""
reduce some useless function and reinforcement pd.to_sql func with insert ignore method

"""

update_information = "reduce some useless function and reinforcement pd.to_sql func with insert ignore method"


# ---------------------


class DfSave(object):
    def __init__(self):
        pass

    @staticmethod
    def df2csv(df, tablename, path='', required_cols=None, **kwargs):
        filepath = '{}.csv'.format(path + tablename)
        if required_cols:
            # filepath = '{}.csv'.format(path + tablename)
            df[required_cols].to_csv(filepath, index=False, header=False, **kwargs)
        else:
            # filepath = '{}.csv'.format(path + tablename)
            df.to_csv(filepath, index=False, header=False, **kwargs)
        return tablename, filepath, df.columns.values.tolist()

    @staticmethod
    def df2hdf5(df, tablename, required_cols=None, path='', **kwargs):

        if required_cols:
            filepath = '{}.h5'.format(path + tablename)
            df[required_cols].to_hdf(filepath, key='df', **kwargs)
        else:
            filepath = '{}.h5'.format(path + tablename, key='df', **kwargs)
            df.to_hdf(filepath, key='df', **kwargs)
        return tablename, filepath, df.columns.values.tolist()

    @staticmethod
    def clear_dir(csv_folder):
        # 清空生成的csv所在文件夹 避免csv未更新完全 部分旧数据 部分新数据的问题
        import os
        import glob

        files = glob.glob(csv_folder)
        for f in files:
            os.remove(f)

    @staticmethod
    def csv2mysql(engine, para, tablename, filepath, columns, header, table_prefix='', exampletable=None, db=None,
                  exampledb=None, field_terminated=',', lines_terminated='\n', auto_incre_col=False):
        """

        :param engine:
        :param para:
        :param tablename:
        :param filepath:
        :param columns:
        :param header:
        :param table_prefix:
        :param exampletable:
        :param db:
        :param exampledb:
        :param field_terminated:
        :param lines_terminated:
        :return:
        """
        # param----------
        #        header = False w为Bool类型
        # table 要插入到数据库的table
        col = ','.join(columns)
        table = table_prefix + tablename
        db = para.db if db is None else db
        exampledb = para.db if exampledb is None else exampledb
        # param----------

        # 连接生成数据到插入数据部分 并记录插入时长
        t = time.time()
        # 同时生成插csv需要的sql语句
        sql_get_existing_tables = " SHOW TABLES FROM {db} LIKE '{table}'".format(db=db, table=table)

        if header:  # 如果csv中没有列名了
            sql2 = """LOAD DATA LOCAL INFILE '{filepath}' INTO TABLE {db}.{table} FIELDS TERMINATED BY '{field_terminated}' LINES TERMINATED BY   '{lines_terminated}' ({columns});""".format(
                filepath=filepath, db=db, table=table, field_terminated=field_terminated,
                lines_terminated=lines_terminated,
                columns=col)
        else:  # 如果csv中有列名了
            sql2 = """LOAD DATA LOCAL INFILE '{filepath}' INTO TABLE {db}.{table} FIELDS TERMINATED BY '{field_terminated}' LINES TERMINATED BY   '{lines_terminated}' IGNORE 1 LINES ({columns});""".format(
                filepath=filepath, db=db, table=table, field_terminated=field_terminated,
                lines_terminated=lines_terminated,
                columns=col)

        # 生成engine 插入数据
        if engine.execute(sql_get_existing_tables).fetchall():
            pass  # 已经有这个table了 是向相应表中增加数据
        else:
            if not exampletable:
                col_df = pd.DataFrame(columns=columns)
                col_df.to_sql(table, engine, if_exists='fail', index=False)
            else:
                sql_check_sample_db = "SHOW TABLES FROM {exampledb} LIKE '{sample}'".format(exampledb=exampledb,
                                                                                            sample=exampletable)
                sql1 = "CREATE TABLE IF NOT EXISTS {db}.{table} LIKE {exampledb}.{exampletable};".format(db=db,
                                                                                                         table=table,
                                                                                                         exampledb=exampledb,
                                                                                                         exampletable=exampletable)
                if engine.execute(sql_check_sample_db).fetchall():
                    engine.execute(sql1)  # 没有这个table 但是有exampletable 创建exampletable
                else:
                    raise ValueError(" No sample table {} in sample db {}".format(exampletable, exampledb))
        if auto_incre_col:
            engine.execute(
                "alter table {} add id BIGINT(255) NOT NULL AUTO_INCREMENT, add primary key (id)".format(table))
        else:
            pass
        engine.execute(sql2)
        t2 = time.time() - t
        print('The insertion of table {} costs '.format(table) + str(t2) + 'seconds')

    @classmethod
    def df2sql(cls, df, tablename, connector, db, table_prefix='', folder_path=None, exampletable=None, exampledb=None,
               auto_incre_col=False, rm_csv=False, field_terminated=',', lines_terminated='\n', ):
        """
        针对每一个df 存一次csv 导一次数据库
        """
        engine = connector._SelfEngine()
        para = connector._para
        tablename, csv_path, cols = cls.df2csv(df, tablename, folder_path)
        DfSave.csv2mysql(engine, para, tablename, csv_path, columns=cols, header=True, table_prefix=table_prefix, db=db,
                         exampletable=exampletable, exampledb=exampledb, field_terminated=field_terminated,
                         lines_terminated=lines_terminated, auto_incre_col=auto_incre_col)
        if rm_csv:
            os.remove(csv_path)
        engine.dispose()


class ConnectMysql(object):
    # C_RR_ResearchReport
    def __init__(self,
                 mysql_name,
                 host='xxx',
                 port=3006,
                 user='xxx',
                 passwd='xxx',
                 charset='UTF8',
                 db='xxx'):

        SQLConnector = namedtuple(mysql_name, ['host', 'port', 'user', 'passwd', 'charset', 'db'])
        self._para = SQLConnector(host, port, user, passwd, charset, db)
        # self.DF2SQL = DfSave

    def CSV2SQL(self, engine, tablename, filepath, columns, header, table_prefix='', exampletable=None, db=None,
                exampledb=None, field_terminated=',', lines_terminated='\n'):

        # engine = self._SelfEngine()
        DfSave.csv2mysql(engine,
                         self._para,
                         tablename,
                         filepath,
                         columns,
                         header,
                         table_prefix=table_prefix,
                         exampletable=exampletable, db=db,
                         exampledb=exampledb, field_terminated=field_terminated,
                         lines_terminated=lines_terminated)
        # engine.dispose()

    def df2sql(self, df, table, db=None, csv_store_path='./data/', auto_incre_col=False, rm_csv=False,
               field_terminated=',', lines_terminated='\n', ):
        """

        :param auto_incre_col:
        :param df:
        :param table:
        :param db:
        :param csv_store_path:
        :return:
        """
        if db is None:
            db = self._para.db
        else:
            pass

        DfSave.df2sql(df,
                      table,
                      self,
                      db,
                      folder_path=csv_store_path,
                      exampletable=None,
                      exampledb=db,
                      auto_incre_col=auto_incre_col, rm_csv=rm_csv, field_terminated=field_terminated,
                      lines_terminated=lines_terminated, )

    def sql2data(self, sql, **kwargs):
        """
        execute SQL statement
        :param sql:
        :param kwargs:
        :return:
        """
        conn = self._SelfConnect()

        df = pd.read_sql(sql, conn, **kwargs)
        conn.close()
        return df

    def DetectConnectStatus(self, returnresult=False, printout=False):
        """
        Detect connect status

        :param returnresult:
        :param printout:
        :return:
        """

        try:
            result = self.Excutesql()
            status = 'Good connection!'
            if printout:
                print(status)
        except pymysql.OperationalError as e:
            result = str(e)
            status = 'Bad connection!'
            if printout:
                print(e)
        except Exception as e:
            result = str(e)
            status = 'Bad connection(Unknown)!'
            if printout:
                print(e)
        finally:
            if returnresult:
                return result
            else:
                return status

    def _SelfConnect(self):
        """
        use pymysql to create connector
        :return: connector
        """
        conn = pymysql.connect(host=self._para.host, port=self._para.port, user=self._para.user,
                               passwd=self._para.passwd,
                               charset=self._para.charset, db=self._para.db, local_infile=1)
        return conn

    def _SelfEngine(self):
        """
        use sqlalchemy to create engine
        :return: engine
        """
        engine = sqlalchemy.create_engine(
            'mysql+pymysql://{}:{}@{}:{}/{}?charset={}&local_infile=1'.format(
                self._para.user, self._para.passwd, self._para.host,
                self._para.port, self._para.db,
                self._para.charset), pool_size=2)  # 用sqlalchemy创建引擎
        # print(engine)
        return engine

    def Excutesql(self, sql='SHOW DATABASES'):
        """
        execute SQL statement,default execute SHOW DATABASES
        Auto close connector

        :param sql: standard sql statement
        :return: result of sql statement
        """
        conn = self._SelfConnect()
        cur = conn.cursor()
        cur.execute(sql)
        result = cur.fetchall()
        cur.close()
        conn.commit()
        conn.close()
        return result

    @staticmethod
    def db_colname(pandas_colname):
        '''convert pandas column name to a DBMS column name
            TODO: deal with name length restrictions, esp for Oracle
        '''
        colname = pandas_colname.replace(' ', '_').strip()
        return colname

    @staticmethod
    def insert_data_file(table, columns, filepath, engine):
        t = time.time()
        # param----------
        # table 要插入到数据库的table
        # 连接生成数据到插入数据部分 并记录插入时长
        # param----------
        # 同时生成插csv需要的sql语句
        #     sql1 = "CREATE TABLE IF NOT EXISTS {} LIKE {};".format(table, exampletable)
        sql2 = """LOAD DATA LOCAL INFILE '{filepath}' INTO TABLE {table} FIELDS TERMINATED BY ',' LINES TERMINATED BY '\n' ({columns});""".format(
            filepath=filepath, table=table, columns=','.join(columns))
        # sql3="ALTER IGNORE TABLE {table_name} ADD UNIQUE INDEX( {unique_index_columns} );".format(table_name=table,unique_index_columns=index)
        # 生成engine 插入数据
        engine.execute(sql2)
        print('The insertion of data {} costs {} seconds'.format(table, str(time.time() - t)))
        engine.dispose()

    def updateinsert(self, data, db, table):
        columns = data.columns

        sql = "INSERT IGNORE INTO {}.{} ({}) VALUES ({})  ".format(db, table, ','.join(columns),
                                                                   ','.join(['%s'] * data.shape[1]))
        conn = self._SelfConnect()
        cur = conn.cursor()
        try:
            cur.executemany(sql, data.values.tolist())
        except pymysql.err.IntegrityError as e:
            print(e)
        else:
            conn.commit()
            cur.close()
            conn.close()
            print('insert process completed!')

    def SHOWDATABASES(self):
        """
        execute SHOW DATABASES command
        :return:
        """
        return self.Excutesql(sql='SHOW DATABASES')

    def SHOWTABLES(self):
        """
        execute SHOW TABLES command
        :return:
        """
        return self.Excutesql(sql='SHOW TABLES')


class MysqlCommands(object):
    @staticmethod
    def alter_table_engine(table, connector, engine='MyISAM', logger=None):
        sql = "ALTER TABLE `{table}` ENGINE = {engine} ".format(table=table, engine=engine)
        connector.Excutesql(sql.format(table=table))
        if logger:
            logger.info("[SQL][{}] ".format(table) + sql.format(table=table))
            logger.info("[SQL][{}] Done".format(table))
        else:
            print("[SQL][{}] ".format(table) + sql.format(table=table))
            print("[SQL][{}] Done".format(table))

    @staticmethod
    def obtain_the_engine_of_table(table, connector, logger=None):
        sql = 'SHOW CREATE TABLE {table}'.format(table=table)
        if logger:
            logger.info("[SQL][{}] ".format(table) + sql)

        return connector.Excutesql(sql)[0][1].split('ENGINE=')[-1].split(' ')[0]

    @staticmethod
    def show_tables_like(like_string, connector, logger=None):
        sql = 'SHOW TABLES LIKE "{}%"'.format(like_string)
        tables = connector.sql2data(sql).values.ravel()
        if logger:
            logger.info("[SQL][{}%] ".format(like_string) + sql)
        return tables

    @staticmethod
    def alter_table_comment(table, COMMENT, connector, logger=None):
        sql = """ALTER TABLE  `{table}` COMMENT =  '{COMMENT}'""".format(table=table, COMMENT=COMMENT)
        connector.Excutesql(sql)
        if logger:
            logger.info("[SQL][{}] ".format(table) + sql)


class MysqlConnEnforcePandas(ConnectMysql):
    def __init__(self,
                 mysql_name,
                 host='xxx',
                 port=3303,
                 user='xxx',
                 passwd='xxx',
                 charset='UTF8',
                 db='xxx'):
        super(MysqlConnEnforcePandas, self).__init__(mysql_name, host, port, user, passwd, charset, db)
        self.MysqlCommands = MysqlCommands
        self.__name__ = self.__class__.__name__

        pass

    @staticmethod
    def sub_shell_process(task):
        time.sleep(random.random())
        command, sql = task

        c = subprocess.Popen(command, shell=True, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)

        c.stdin.write(bytes(sql, encoding='utf8'))
        #  subprocess.Popen(
        #         command, stdin = input_file, stderr=subprocess.PIPE, stdout=subprocess.PIPE )
        result = c.communicate()
        c.wait()
        c.terminate()
        return result

    def _Load_Data_INFILE_command_creator(self, col, table, csv_path):
        connector_command = 'mysql -h{host} -u{user} -p{passwd} --database={db}'.format(host=self._para.host,
                                                                                        user=self._para.host.user,
                                                                                        passwd=self._para.host.passwd,
                                                                                        db=self._para.host.db)
        # sql1 = "create table if not exists {} like {};".format(table, exampletable)
        sql2 = """LOAD DATA LOCAL INFILE '{filepath}' INTO TABLE {table} FIELDS TERMINATED BY ',' LINES TERMINATED BY '\n' ({col})""".format(
            filepath=csv_path, table=table, col=','.join(col))

        return connector_command, sql2

    @staticmethod
    def chunkdf(df, chunksize=10000):
        import math
        n = int(math.ceil(len(df) / float(chunksize)))
        return (df[i * chunksize:i * chunksize + chunksize] for i in range(n))

    @staticmethod
    def timestamp_parser(df, timestamp_column, time_format='%Y-%m-%d'):
        def strftime_in_series(x): return x.strftime(time_format)

        for ts_col in timestamp_column:
            df[ts_col] = df[ts_col].map(strftime_in_series)
        return df

    @staticmethod
    def _grab_mysql_column_special_type(table, sd, Type='datetime'):
        dfss = sd.sql2data('desc {} '.format(table))
        datetime_col = dfss[dfss.Type == Type]['Field'].values.tolist()
        return datetime_col

    @classmethod
    def _dataframe_adaptation_default(cls, df, table, SD):
        datetime_col = cls._grab_mysql_column_special_type(table, SD)
        df = cls.timestamp_parser(df, datetime_col)
        return df

    @classmethod
    def adaptation(cls, df, table, SD, func=None):
        return (cls._dataframe_adaptation_default if func is None else func)(df, table, SD)

    # def to_sql(self, df, table, **kwargs):
    #     self._to_sql(df, table, self, **kwargs)


class MySQLNode(MysqlConnEnforcePandas):
    pass


MySQLNodeName = 'MySQLNode'
if __name__ == "__main__":
    hs = MysqlConnEnforcePandas('hs')
    s = dict(hs.sql2data('select * from C_ED_IDXSystemConst where LB = 17')[['DM', 'MS']].values)
    print(s)
    pass
