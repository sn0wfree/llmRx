# coding=utf-8


from pipeservice import conf


def load_mysql(file_path):
    import yaml
    with open(file_path, 'r') as f:
        return yaml.load(f)['MySQL']


visits_mysql_node_conf = load_mysql(conf.__path__[0] + '/settings.yml')

CODE = 'asduashduhaskhaskhakhdksa'
