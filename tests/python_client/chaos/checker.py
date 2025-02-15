from enum import Enum
from random import randint
import datetime
from time import sleep
from delayed_assert import expect
from base.collection_wrapper import ApiCollectionWrapper
from common import common_func as cf
from common import common_type as ct
from chaos import constants

from common.common_type import CheckTasks
from utils.util_log import test_log as log


class Op(Enum):
    create = 'create'
    insert = 'insert'
    flush = 'flush'
    index = 'index'
    search = 'search'
    query = 'query'

    unknown = 'unknown'


timeout = 20
enable_traceback = False


class Checker:
    """
    A base class of milvus operation checker to
       a. check whether milvus is servicing
       b. count operations and success rate
    """
    def __init__(self):
        self._succ = 0
        self._fail = 0
        self.c_wrap = ApiCollectionWrapper()
        self.c_wrap.init_collection(name=cf.gen_unique_str('Checker_'),
                                    schema=cf.gen_default_collection_schema(),
                                    timeout=timeout,
                                    enable_traceback=enable_traceback)
        self.c_wrap.insert(data=cf.gen_default_list_data(nb=constants.ENTITIES_FOR_SEARCH),
                           timeout=timeout,
                           enable_traceback=enable_traceback)
        self.initial_entities = self.c_wrap.num_entities    # do as a flush

    def total(self):
        return self._succ + self._fail

    def succ_rate(self):
        return self._succ / self.total() if self.total() != 0 else 0

    def reset(self):
        self._succ = 0
        self._fail = 0


class SearchChecker(Checker):
    """check search operations in a dependent thread"""
    def __init__(self):
        super().__init__()
        self.c_wrap.load(enable_traceback=enable_traceback)  # do load before search

    def keep_running(self):
        while True:
            search_vec = cf.gen_vectors(5, ct.default_dim)
            _, result = self.c_wrap.search(
                data=search_vec,
                anns_field=ct.default_float_vec_field_name,
                param={"nprobe": 32},
                limit=1, timeout=timeout,
                enable_traceback=enable_traceback,
                check_task=CheckTasks.check_nothing
            )
            if result:
                self._succ += 1
            else:
                self._fail += 1
            sleep(constants.WAIT_PER_OP / 10)


class InsertFlushChecker(Checker):
    """check Insert and flush operations in a dependent thread"""
    def __init__(self, flush=False):
        super().__init__()
        self._flush = flush
        self.initial_entities = self.c_wrap.num_entities

    def keep_running(self):
        while True:
            _, insert_result = \
                self.c_wrap.insert(data=cf.gen_default_list_data(nb=constants.DELTA_PER_INS),
                                   timeout=timeout,
                                   enable_traceback=enable_traceback,
                                   check_task=CheckTasks.check_nothing)
            if not self._flush:
                if insert_result:
                    self._succ += 1
                else:
                    self._fail += 1
                sleep(constants.WAIT_PER_OP / 10)
            else:
                # call flush in property num_entities
                t0 = datetime.datetime.now()
                num_entities = self.c_wrap.num_entities
                tt = datetime.datetime.now() - t0
                log.info(f"flush time cost: {tt}")
                if num_entities == (self.initial_entities + constants.DELTA_PER_INS):
                    self._succ += 1
                    self.initial_entities += constants.DELTA_PER_INS
                else:
                    self._fail += 1


class CreateChecker(Checker):
    """check create operations in a dependent thread"""
    def __init__(self):
        super().__init__()

    def keep_running(self):
        while True:
            _, result = self.c_wrap.init_collection(
                name=cf.gen_unique_str("CreateChecker_"),
                schema=cf.gen_default_collection_schema(),
                timeout=timeout,
                enable_traceback=enable_traceback,
                check_task=CheckTasks.check_nothing)
            if result:
                self._succ += 1
                self.c_wrap.drop(timeout=timeout, enable_traceback=enable_traceback)
            else:
                self._fail += 1
            sleep(constants.WAIT_PER_OP / 10)


class IndexChecker(Checker):
    """check Insert operations in a dependent thread"""
    def __init__(self):
        super().__init__()
        self.c_wrap.insert(data=cf.gen_default_list_data(nb=5 * constants.ENTITIES_FOR_SEARCH),
                           timeout=timeout, enable_traceback=enable_traceback)
        log.debug(f"Index ready entities: {self.c_wrap.num_entities }")  # do as a flush before indexing

    def keep_running(self):
        while True:
            _, result = self.c_wrap.create_index(ct.default_float_vec_field_name,
                                                 constants.DEFAULT_INDEX_PARAM,
                                                 name=cf.gen_unique_str('index_'),
                                                 timeout=timeout,
                                                 enable_traceback=enable_traceback,
                                                 check_task=CheckTasks.check_nothing)
            if result:
                self._succ += 1
                self.c_wrap.drop_index(timeout=timeout, enable_traceback=enable_traceback)
            else:
                self._fail += 1


class QueryChecker(Checker):
    """check query operations in a dependent thread"""
    def __init__(self):
        super().__init__()
        self.c_wrap.load(enable_traceback=enable_traceback)  # load before query

    def keep_running(self):
        while True:
            int_values = []
            for _ in range(5):
                int_values.append(randint(0, constants.ENTITIES_FOR_SEARCH))
            term_expr = f'{ct.default_int64_field_name} in {int_values}'
            _, result = self.c_wrap.query(term_expr, timeout=timeout,
                                          enable_traceback=enable_traceback,
                                          check_task=CheckTasks.check_nothing)
            if result:
                self._succ += 1
            else:
                self._fail += 1
            sleep(constants.WAIT_PER_OP / 10)


def assert_statistic(checkers, expectations={}):
    for k in checkers.keys():
        # expect succ if no expectations
        succ_rate = checkers[k].succ_rate()
        total = checkers[k].total()
        if expectations.get(k, '') == constants.FAIL:
            log.info(f"Expect Fail: {str(k)} succ rate {succ_rate}, total: {total}")
            expect(succ_rate < 0.49 or total < 2,
                   f"Expect Fail: {str(k)} succ rate {succ_rate}, total: {total}")
        else:
            log.info(f"Expect Succ: {str(k)} succ rate {succ_rate}, total: {total}")
            expect(succ_rate > 0.90 or total > 2,
                   f"Expect Succ: {str(k)} succ rate {succ_rate}, total: {total}")