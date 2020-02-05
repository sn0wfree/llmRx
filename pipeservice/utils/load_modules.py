# coding=utf-8
import glob
import importlib
import os


def load_modules(path, py_type='/queue_*.py', return_name=True):
    for f in glob.glob(path.rstrip('/') + py_type):
        name = os.path.splitext(os.path.split(f)[1])[0]
        module_name = name
        file_path = f
        spec = importlib.util.spec_from_file_location(module_name, file_path)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        globals()[module_name] = module
        if return_name:
            yield module_name
        else:
            yield module


if __name__ == '__main__':
    a = load_modules('/visits/pipeservice/')
    b = list(a)
    pass
