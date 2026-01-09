import { IsOptional, IsString, ValidateNested } from 'class-validator';
import { PaginationQueryDto } from './pagination-query.dto';
import { SortByDto } from './sort-by.dto';
import { Type } from 'class-transformer';
import { ValidationMessages } from '../../constants/validation-messages';
import { Fields } from '../../constants/fields';

export class BaseQueryDto extends PaginationQueryDto {
  @IsOptional()
  @IsString({
    message: ValidationMessages.String,
    context: { field: Fields.General.Search },
  })
  search?: string;

  @IsOptional()
  @ValidateNested()
  @Type(() => SortByDto)
  orderBy?: SortByDto[];
}
