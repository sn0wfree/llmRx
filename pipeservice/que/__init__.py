# coding=utf-8


def create_queue_type1(name, table, db, visits_mysql_node_conf, ext_process_func=None, maxlen=10):
    from pipeservice.service.TaskQueue import TaskQueue
    from pipeservice.service.getTasks import GetTasks

    if ext_process_func is None:
        def ext_process(df):
            return df['full_url'].values.tolist()

        ext_process = ext_process
    else:
        ext_process = ext_process_func

    # reset_tasks(table, db, visits_mysql_node_conf)()
    Tasks = GetTasks(table, db, visits_mysql_node_conf, maxlen=maxlen, ext_process=ext_process)
    Tasks.reset_tasks()
    queue = TaskQueue(name, Tasks, max_len=maxlen)
    return queue


if __name__ == '__main__':
    pass
