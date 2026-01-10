import { QueryParams, TimeRangedQueryParams } from '@/shared/application/query';
import { BaseQueryDto } from '../dto/base-query.dto';
import { TimeRangedQueryDto } from '../dto/time-ranged.query.dto';

export class QueryParamsMapper {
  static fromDto(dto: BaseQueryDto): QueryParams {
    return {
      page: dto.page,
      limit: dto.limit,
      search: dto.search,
      orderBy: dto.orderBy,
    };
  }

  static fromTimeRangedDto(dto: TimeRangedQueryDto): TimeRangedQueryParams {
    return {
      ...this.fromDto(dto),
      startDate: dto.startDate,
      endDate: dto.endDate,
    };
  }
}
