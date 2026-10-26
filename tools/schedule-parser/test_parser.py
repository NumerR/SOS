from parser import ScheduleParser

sample = {
    "lessonTimes": [
        {"id": 1, "hour_from": 11, "minute_from": 20, "hour_to": 12, "minute_to": 50, "fullName": "11:20 – 12:50"},
        {"id": 2, "hour_from": 8, "minute_from": 30, "hour_to": 10, "minute_to": 0, "fullName": "08:30 – 10:00"},
    ],
    "lessons": [
        {"date": "07.09.2026", "lesson_time_id": 1, "groupName": "Б-ИСиТ-22", "discipline": "Поздняя", "class_type_name": "Лекция", "classroom": "201", "staffNames": ["Иванов И.И."]},
        {"date": "07.09.2026", "lesson_time_id": 2, "groupName": "Б-ИСиТ-22", "discipline": "Ранняя", "class_type_name": "Практика", "classroom": "101", "staffNames": ["Петров П.П."]},
    ],
}

lessons = ScheduleParser('Б-ИСиТ-22').parse_api(sample)
assert [x.time for x in lessons] == ['08:30 – 10:00', '11:20 – 12:50']
assert [x.discipline for x in lessons] == ['Ранняя', 'Поздняя']
print('OK: API parser sorts lessons by actual start time.')
