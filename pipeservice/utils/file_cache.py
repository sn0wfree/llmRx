# coding=utf-8
import datetime
import hashlib
import os
import pickle
from collections import OrderedDict
from functools import wraps


def get_cache_path(fmt="%Y-%m-%d"):
    __cache__path = '/tmp/{}/'.format(datetime.datetime.now().strftime(format=fmt))
    if not os.path.exists(__cache__path):
        os.mkdir(__cache__path)
    return __cache__path


def prepare_args(func, arg, kwargs, time_format_dimension='%Y-%m-%d'):
    kwargs = OrderedDict(sorted(kwargs.items(), key=lambda t: t[0]))  # sort kwargs to fix hashcode if sample input
    func_name = func.__code__.co_name
    key = pickle.dumps([func_name, arg, kwargs])  # get the unique key for the same input
    name = func_name + "_" + hashlib.sha1(key).hexdigest() + "_{}".format(
        datetime.datetime.now().strftime(time_format_dimension))  # create cache file name
    file_path = get_cache_path()
    return file_path, name


def _cache(func, arg, kwargs, time_format_dimension='%Y-%m-%d'):
    file_path, name = prepare_args(func, arg, kwargs, time_format_dimension=time_format_dimension)
    if os.path.exists(file_path + name):
        with open(file_path + name, 'rb') as f:
            res = pickle.load(f)
    else:
        res = func(*arg, **kwargs)
        with open(file_path + name, 'wb') as f:
            pickle.dump(res, f)
    return res


def file_cache(deco_arg_dict):
    if callable(deco_arg_dict):
        @wraps(deco_arg_dict)
        def wrapped(*args, **kwargs):
            return _cache(deco_arg_dict, args, kwargs)

        return wrapped
    else:
        def _deco(func):
            @wraps(func)
            def __deco(*args, **kwargs):
                return _cache(func, args, kwargs)

            return __deco

        return _deco


if __name__ == '__main__':
    @file_cache
    def test1(a, b):
        return a + b


    @file_cache()
    def test2(a, b, c):
        return a + b * c


    pass
