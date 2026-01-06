import { IsOptional, IsString, ValidateNested } from 'class-validator';
import { PaginationQueryDto } from './pagination-query.dto';
import { SortByDto } from './sort-by.dto';
import { Type } from 'class-transformer';
import { VALIDATION_MESSAGES } from '../constants/validation-messages';
import { FIELDS } from '../constants/fields';

export class BaseQueryDto extends PaginationQueryDto {
  @IsOptional()
  @IsString({
    message: VALIDATION_MESSAGES.STRING,
    context: { field: FIELDS.GENERAL.SEARCH },
  })
  search?: string;

  @IsOptional()
  @ValidateNested()
  @Type(() => SortByDto)
  orderBy?: SortByDto[];
}
