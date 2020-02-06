# coding=utf-8

import responder

from pipeservice import que
# from visits.pipeservice.queue_news import Queue_obj
from pipeservice.conf.conf import CODE
from pipeservice.utils.load_modules import load_modules


def load_api_settings(API_folder):
    Queue_module_list = load_modules(API_folder, py_type='/queue_*.py', return_name=False)
    Queue_obj_list = list(map(lambda x: x.Queue, Queue_module_list))
    EXISTS_TASKS_DICT = dict(map(lambda x: (x.name, x), Queue_obj_list))
    return EXISTS_TASKS_DICT


def build_api(address='0.0.0.0', port=5000, API_folder=que.__path__[0]):
    api = responder.API()
    EXISTS_TASKS_DICT = load_api_settings(API_folder)
    print(EXISTS_TASKS_DICT)

    @api.route("/{get_task_id}/done/{self_id}/{checker}")
    async def check_task(req, resp, *, get_task_id, self_id, checker):
        if checker != CODE:
            resp.status_code = 404
            resp.text = 'Not Allowed to do'
        else:
            if get_task_id not in list(EXISTS_TASKS_DICT.keys()):
                resp.status_code = 416
                resp.text = f'Tasks {get_task_id} not at list'
            else:

                queue_test = EXISTS_TASKS_DICT.get(get_task_id)
                if queue_test.status == 'End':
                    resp.status_code = 414
                    resp.text = f'Tasks {get_task_id}  has end'
                else:
                    try:
                        queue_test.load_func.update_task(self_id)
                        resp.text = f'task {self_id} updated'
                    except Exception as e:
                        resp.status_code = 417
                        resp.text = str(e)

    @api.route("/{get_task_id}/get/{checker}")
    async def get_task(req, resp, *, get_task_id, checker):
        if checker != CODE:
            resp.status_code = 404
            resp.text = 'Not Allowed to do'
        elif get_task_id not in list(EXISTS_TASKS_DICT.keys()):
            resp.status_code = 416
            resp.text = f'Tasks {get_task_id} not at list'
        else:
            queue_test = EXISTS_TASKS_DICT.get(get_task_id)
            if queue_test.status == 'End':
                resp.status_code = 414
                resp.text = f'Tasks {get_task_id}  has end'
            else:
                try:
                    resp.media = queue_test.get()
                except IndexError as e:
                    resp.status_code = 416
                    resp.text = f'Not Found available tasks for {get_task_id}'

    api.run(address=address, port=port)


if __name__ == '__main__':
    build_api(address='0.0.0.0', port=5000)
