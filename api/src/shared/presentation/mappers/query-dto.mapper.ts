import { BaseQueryDto } from '../dto/base-query.dto';
import { TimeRangedQueryDto } from '../dto/time-ranged.query.dto';
import { FindAllOptions } from '../../domain/specifications/find-all-options.model';
import { TimeRangedFindAllOptions } from '../../domain/specifications/time-ranged-find-all-options.model';

export class QueryDtoMapper {
  static toFindAllOptions(dto: BaseQueryDto): FindAllOptions {
    return FindAllOptions.create({
      page: dto.page,
      limit: dto.limit,
      orderBy: dto.orderBy,
      search: dto.search,
    });
  }

  static toTimeRangedFindAllOptions(
    dto: TimeRangedQueryDto,
  ): TimeRangedFindAllOptions {
    return TimeRangedFindAllOptions.create({
      page: dto.page,
      limit: dto.limit,
      orderBy: dto.orderBy,
      search: dto.search,
      startDate: dto.startDate,
      endDate: dto.endDate,
    });
  }
}
