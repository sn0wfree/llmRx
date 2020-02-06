# coding=utf-8
from pipeservice.service.TaskQueue import TaskQueue
from pipeservice.service.getTasks import GetTasks


def ext_process(df):
    c = df[['full_url', 'self_id']].to_dict('records')
    print(c)
    return c


def create_queue_type1(name, table, db, visits_mysql_node_conf, ext_process_func=None, maxlen=10):
    if ext_process_func is None:
        ext_process_func = ext_process
    else:
        ext_process_func = ext_process_func

    # reset_tasks(table, db, visits_mysql_node_conf)()
    tasks = GetTasks(table, db, visits_mysql_node_conf, maxlen=maxlen, ext_process=ext_process_func)
    tasks.reset_tasks()
    queue = TaskQueue(name, tasks, max_len=maxlen)
    return queue


if __name__ == '__main__':
    pass
