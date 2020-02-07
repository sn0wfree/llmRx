FROM python:3.7.6-slim-stretch

RUN mkdir /code
COPY . /code

WORKDIR /code




RUN cd /code && apt-get update \
    && apt-get install -y --no-install-recommends gcc  \
    && rm -rf /var/lib/apt/lists/* \
    && pip install -r requirements.txt \
    && apt-get purge -y --auto-remove gcc


CMD python ./pipeservice/__init__.py $0 $@