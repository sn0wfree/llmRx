# coding=utf-8

from collections import deque

MAXLEN = 10


class TaskQueue(object):
    def __init__(self, name, load_func, max_len=MAXLEN):
        """

        :param name:  queue name
        :param load_func:  generator for load task
        :param max_len: max length of a queue
        """
        self.name = name
        self.deque = deque(maxlen=max_len)
        if callable(load_func):
            self.load_func = load_func  # MySQLNode('pages', **visits_mysql_node_conf)
        else:
            raise ValueError('load_func only accept  callable obj')
        self.empty_counts = 0
        self.status = 'Good'

    def load(self):
        tasks = self.load_func()
        return tasks

    def get(self):
        if len(self.deque) == 0:
            tasks = self.load()
            if tasks is None:
                self.status = 'End'
            else:
                self.put(tasks)
        else:
            pass

        item = self.deque.pop()  # 从右端移除元素
        return item

    def put(self, element):
        if isinstance(element, (list, tuple, set)):
            self.deque.extendleft(element)
        elif isinstance(element, dict):
            self.deque.appendleft(element)  # 从左端添加一个元素
        else:
            raise ValueError(f'unsupported element type! only accept Task, list, tuple, but get {element}')


if __name__ == '__main__':
    pass
